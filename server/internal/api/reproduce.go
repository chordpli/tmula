package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/safety"
)

// Reproduce sentinels, so the HTTP handler can map each refusal to the right
// status without string-matching, and Go-level callers (the CLI tests) can
// assert on the cause.
var (
	// errRunNotFound: the run id is known to neither the cache nor the store.
	errRunNotFound = errors.New("run not found")
	// errFindingNotFound: the run exists but has no finding under that
	// (category, evidenceRef) key.
	errFindingNotFound = errors.New("finding not found")
	// errNotReproducible: the finding cannot be replayed — it carries no
	// session coordinates (e.g. a summary-derived finding), or its category
	// needs inputs the replay path does not generate (mutation).
	errNotReproducible = errors.New("finding cannot be reproduced")
	// errSpecUnavailable: the run's spec is gone. Specs live in engine memory
	// only (they are not persisted), so an evicted run or a restarted engine
	// cannot replay its sessions.
	errSpecUnavailable = errors.New("run spec no longer available")
)

// Reproduce attempt bounds: enough repeats to tell flaky from deterministic
// without letting a typo replay a session hundreds of times.
const (
	defaultReproduceAttempts = 3
	maxReproduceAttempts     = 20
)

// reproduceNote spells out the limits of a reproduce verdict on every result.
// The replay is deterministic about WHAT the session sent (same seed, same
// walk), not WHEN: the original interleaving, concurrency and target state are
// gone, so the verdict is a strong signal, never a proof.
const reproduceNote = "reproduce replays the session's traffic composition (same seed, same walk) in isolation; it does not recreate the original timing, concurrency or target state — treat the verdict as a signal, not a proof"

// ReproduceRequest selects which of a run's findings to replay and how often.
type ReproduceRequest struct {
	Category    domain.FindingCategory `json:"category"`
	EvidenceRef string                 `json:"evidenceRef"`
	// Attempts is how many isolated replays to run; 0 means the default (3).
	Attempts int `json:"attempts,omitempty"`
}

// ReproduceStep is one request of a replayed session.
type ReproduceStep struct {
	Node       domain.ID `json:"node"`
	StatusCode int       `json:"statusCode,omitempty"`
	LatencyMs  float64   `json:"latencyMs"`
	ErrorClass string    `json:"errorClass,omitempty"`
	// Matched reports whether this step carried the finding's signal (the
	// contract 5xx on the finding's API, a request over the p95 gate, ...).
	Matched bool `json:"matched,omitempty"`
}

// ReproduceAttempt is one isolated replay of the evidence session.
type ReproduceAttempt struct {
	// Reproduced is true when at least one step carried the finding's signal.
	Reproduced bool            `json:"reproduced"`
	Steps      []ReproduceStep `json:"steps"`
}

// ReproduceResult is the outcome of replaying one finding's evidence session.
type ReproduceResult struct {
	RunID       domain.ID              `json:"runId"`
	Category    domain.FindingCategory `json:"category"`
	EvidenceRef string                 `json:"evidenceRef"`
	// Session is the evidence session that was replayed (its wire name "vu"
	// matches the evidence bundle's masker-safe naming).
	Session domain.EvidenceSession `json:"vu"`
	// RunSeed is the original run's seed, so the output can show the
	// coordinate arithmetic (session seed = run seed + user index).
	RunSeed    int64              `json:"runSeed"`
	Attempts   []ReproduceAttempt `json:"attempts"`
	Reproduced int                `json:"reproduced"`
	// RootCauseClass is the verdict: functional (reproduced every attempt),
	// load-dependent (none) or flaky (some). The same value is stamped on the
	// run's stored finding.
	RootCauseClass string `json:"rootCauseClass"`
	// Note restates the verdict's limits (see reproduceNote).
	Note string `json:"note"`
}

// ReproduceFinding replays one evidence session of the run's (category,
// evidenceRef) finding in isolation — single session, no concurrent load — and
// classifies the root cause from how often the finding's signal recurred:
// every attempt → functional (the bug does not need load), none → load-
// dependent (it needs the original concurrency/saturation), some → flaky. The
// verdict is annotated on the stored finding (RootCauseClass) so reports keep
// it.
//
// The replay deterministically re-derives the session's walk from its seed
// coordinates and runs under the full safety policy rebuilt from the engine's
// current spec — allowlist, prod-lock, rate cap — so a target that is no
// longer safe to touch is refused before any traffic is sent. What it does NOT
// recreate is timing: the verdict is a signal, not a proof (see
// reproduceNote), and a target whose state changed since the run may answer
// differently.
func (s *Server) ReproduceFinding(ctx context.Context, runID domain.ID, req ReproduceRequest) (ReproduceResult, error) {
	attempts := req.Attempts
	if attempts == 0 {
		attempts = defaultReproduceAttempts
	}
	if attempts < 1 || attempts > maxReproduceAttempts {
		return ReproduceResult{}, fmt.Errorf("api: reproduce attempts must be 1..%d, got %d", maxReproduceAttempts, req.Attempts)
	}

	rep, ok := s.reportFor(runID)
	if !ok {
		return ReproduceResult{}, fmt.Errorf("%w: %q", errRunNotFound, runID)
	}
	finding := findingByKey(rep.Findings, req.Category, req.EvidenceRef)
	if finding == nil {
		return ReproduceResult{}, fmt.Errorf("%w: run %q has no finding %s/%s", errFindingNotFound, runID, req.Category, req.EvidenceRef)
	}
	if finding.Category == domain.FindingMutation {
		return ReproduceResult{}, fmt.Errorf("%w: mutation findings need the mutated inputs, which the replay path does not generate", errNotReproducible)
	}
	if finding.Evidence == nil || len(finding.Evidence.Sessions) == 0 {
		return ReproduceResult{}, fmt.Errorf("%w: it carries no session coordinates (summary-aggregated runs retain none)", errNotReproducible)
	}
	// The earliest representative — where the issue first surfaced (evidence
	// sessions are ordered earliest-first, see obs.evidenceSessions).
	sess := finding.Evidence.Sessions[0]

	s.mu.Lock()
	spec, ok := s.specs[rep.Run.ExperimentID]
	s.mu.Unlock()
	if !ok {
		return ReproduceResult{}, fmt.Errorf("%w: specs are held in engine memory only — the run was evicted or the engine restarted; re-run the experiment to reproduce against it", errSpecUnavailable)
	}

	// Full safety policy, rebuilt from the current spec exactly as StartRun
	// builds it: allowlist, prod-lock refusal, rate/concurrency cap.
	guard, err := safety.NewGuardForEnv(spec.TargetEnv, nil, false)
	if err != nil {
		return ReproduceResult{}, &guardError{err: err}
	}
	if err := guard.AllowHost(spec.TargetEnv.BaseURL); err != nil {
		return ReproduceResult{}, &guardError{err: err}
	}

	start, maxSteps, err := sessionJourney(spec, sess)
	if err != nil {
		return ReproduceResult{}, err
	}
	user, err := sessionUser(ctx, spec, sess)
	if err != nil {
		return ReproduceResult{}, err
	}
	match := findingSignal(*finding, spec)
	if match == nil {
		return ReproduceResult{}, fmt.Errorf("%w: category %q has no replay signal", errNotReproducible, finding.Category)
	}

	// The runner mirrors the original run's traffic-shaping knobs (deviation
	// flows into the seeded walk; think time is skipped — it paces, it does
	// not steer, and an isolated replay should be fast). The correlation run
	// id gets a "-repro" suffix so target-side logs can tell the replay from
	// the original run, while the session id stays the exact value the
	// evidence told the operator to grep for.
	runner := load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, spec.Templates,
		load.WithGuard(guard),
		load.WithCorrelationIDs(runID+"-repro", scenarioIDForSpec(spec)),
		load.WithDeviation(spec.Experiment.Params.DeviationRate),
	)

	res := ReproduceResult{
		RunID: runID, Category: finding.Category, EvidenceRef: finding.EvidenceRef,
		Session: sess, RunSeed: spec.Seed, Note: reproduceNote,
	}
	for i := 0; i < attempts; i++ {
		results, err := runner.RunSession(ctx, spec.Graph, start, maxSteps, user, sess.Seed, nil)
		if err != nil {
			return ReproduceResult{}, fmt.Errorf("api: reproduce attempt %d: %w", i+1, err)
		}
		if ctx.Err() != nil {
			return ReproduceResult{}, fmt.Errorf("api: reproduce attempt %d: %w", i+1, ctx.Err())
		}
		at := buildAttempt(results, match)
		res.Attempts = append(res.Attempts, at)
		if at.Reproduced {
			res.Reproduced++
		}
	}
	res.RootCauseClass = rootCauseClass(res.Reproduced, attempts)
	s.annotateRootCause(runID, finding.Category, finding.EvidenceRef, res.RootCauseClass)
	return res, nil
}

// findingByKey returns the finding with the given (category, evidenceRef)
// identity — the same key the run comparison and the baseline gate use — or
// nil.
func findingByKey(fs []domain.Finding, cat domain.FindingCategory, ref string) *domain.Finding {
	for i := range fs {
		if fs[i].Category == cat && fs[i].EvidenceRef == ref {
			return &fs[i]
		}
	}
	return nil
}

// sessionJourney resolves the entry node and step bound the evidence session
// walked with: the run defaults, overridden by the persona segment the session
// was drawn from — exactly how the open-model scheduler resolved them when the
// session first ran. A persona that is no longer a segment of the spec is an
// error: silently replaying a different journey would corrupt the verdict.
func sessionJourney(spec RunSpec, sess domain.EvidenceSession) (domain.ID, int, error) {
	start, maxSteps := spec.Start, spec.MaxSteps
	if sess.Persona == "" {
		return start, maxSteps, nil
	}
	for _, seg := range spec.Segments {
		if seg.Name != sess.Persona {
			continue
		}
		if seg.Start != "" {
			start = seg.Start
		}
		if seg.MaxSteps > 0 {
			maxSteps = seg.MaxSteps
		}
		return start, maxSteps, nil
	}
	return "", 0, fmt.Errorf("%w: persona %q is not a segment of the run's spec, so its journey cannot be replayed", errNotReproducible, sess.Persona)
}

// sessionUser rebuilds the virtual user the session ran as: the same id (so
// the X-Tmula-Session-ID header matches what is already in the target's logs)
// and, when the spec authenticates, the credential the session's index
// selected — the same pure Acquire keying both run paths use. Closed sessions
// additionally reuse their pool user's template vars.
func sessionUser(ctx context.Context, spec RunSpec, sess domain.EvidenceSession) (load.VirtualUser, error) {
	user := load.VirtualUser{ID: sess.SessionID}
	if spec.IsOpen() {
		if len(spec.Users) > 0 {
			user.Cred = spec.Users[0].Cred
			user.Vars = spec.Users[0].Vars
		}
	} else if pool := spec.ClosedUsers(); sess.UserIndex >= 0 && sess.UserIndex < int64(len(pool)) {
		user.Cred = pool[sess.UserIndex].Cred
		user.Vars = pool[sess.UserIndex].Vars
	}
	provider, err := spec.CredentialProvider()
	if err != nil {
		return load.VirtualUser{}, err
	}
	if provider != nil {
		// Closed users are keyed by pool index; open arrivals are 1-based, so
		// the scheduler keyed them by arrival-1.
		idx := int(sess.UserIndex)
		if spec.IsOpen() && idx > 0 {
			idx--
		}
		cred, err := provider.Acquire(ctx, idx)
		if err != nil {
			return load.VirtualUser{}, fmt.Errorf("api: acquire credential for reproduce: %w", err)
		}
		user.Cred = cred
	}
	return user, nil
}

// findingSignal returns the per-step predicate that decides whether a replayed
// request carried the finding's signal. The predicates mirror the classifier /
// evidence-candidate ones (obs), so the reproduce verdict cannot use a
// different definition of "failed" than the finding it replays. The threshold
// evidence refs are the stable metric identities the comparison keys on
// ("error-rate" / "p95-latency", see obs).
func findingSignal(f domain.Finding, spec RunSpec) func(load.StepResult) bool {
	switch f.Category {
	case domain.FindingContract:
		api := domain.ID(f.EvidenceRef)
		return func(r load.StepResult) bool {
			return r.NodeID == api && r.Err == nil && r.Resp.StatusCode >= 500
		}
	case domain.FindingAvailability:
		api := domain.ID(f.EvidenceRef)
		return func(r load.StepResult) bool {
			return r.NodeID == api && (r.Err != nil || r.Resp.StatusCode >= 500)
		}
	case domain.FindingThreshold:
		switch f.EvidenceRef {
		case "error-rate":
			return func(r load.StepResult) bool {
				return r.Err != nil || r.Resp.StatusCode >= 400
			}
		case "p95-latency":
			gate := spec.Findings.ClassifyConfig().P95LatencyMs
			return func(r load.StepResult) bool {
				return gate > 0 && r.Err == nil && r.Resp.LatencyMs > gate
			}
		}
	}
	return nil
}

// buildAttempt condenses one replayed session into its per-step view and
// whether any step carried the finding's signal.
func buildAttempt(results []load.StepResult, match func(load.StepResult) bool) ReproduceAttempt {
	at := ReproduceAttempt{Steps: make([]ReproduceStep, 0, len(results))}
	for _, r := range results {
		st := ReproduceStep{
			Node:       r.NodeID,
			StatusCode: r.Resp.StatusCode,
			LatencyMs:  r.Resp.LatencyMs,
			ErrorClass: errorClass(r),
			Matched:    match(r),
		}
		if st.Matched {
			at.Reproduced = true
		}
		at.Steps = append(at.Steps, st)
	}
	return at
}

// rootCauseClass maps the reproduce tally to its verdict: every attempt
// failing without load points at a functional bug, none at a load-dependent
// one, anything in between is flaky/uncertain.
func rootCauseClass(reproduced, attempts int) string {
	switch reproduced {
	case attempts:
		return domain.RootCauseFunctional
	case 0:
		return domain.RootCauseLoadDependent
	default:
		return domain.RootCauseFlaky
	}
}

// annotateRootCause stamps the verdict on the run's stored finding (the system
// of record a rebuilt report reads) and on the live in-memory copy when the
// run is still cached, so both views agree. The store write is best-effort,
// like persistRun: a backend hiccup must not fail the result the operator
// already has — it is logged instead.
func (s *Server) annotateRootCause(runID domain.ID, cat domain.FindingCategory, ref, class string) {
	s.mu.Lock()
	rs := s.runs[runID]
	s.mu.Unlock()
	if rs != nil {
		rs.mu.Lock()
		for i := range rs.findings {
			if rs.findings[i].Category == cat && rs.findings[i].EvidenceRef == ref {
				rs.findings[i].RootCauseClass = class
			}
		}
		rs.mu.Unlock()
	}
	if s.store == nil {
		return
	}
	findings, err := s.store.Findings(runID)
	if err != nil {
		slog.Warn("annotate root cause: load findings failed", "run", runID, "err", err)
		return
	}
	changed := false
	for i := range findings {
		if findings[i].Category == cat && findings[i].EvidenceRef == ref {
			findings[i].RootCauseClass = class
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := s.store.SaveFindings(runID, findings); err != nil {
		slog.Error("annotate root cause: save findings failed", "run", runID, "err", err)
	}
}

// reproduceFinding implements POST /runs/{id}/reproduce: decode the request,
// replay, and map each refusal to its status — 404 unknown run/finding, 403
// safety rejection, 410 spec gone (held in memory only), 422 finding not
// replayable, 400 bad request.
func (s *Server) reproduceFinding(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var req ReproduceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode: %w", err))
		return
	}
	res, err := s.ReproduceFinding(r.Context(), id, req)
	if err != nil {
		var ge *guardError
		switch {
		case errors.Is(err, errRunNotFound), errors.Is(err, errFindingNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.As(err, &ge):
			writeErr(w, http.StatusForbidden, ge.err)
		case errors.Is(err, errSpecUnavailable):
			writeErr(w, http.StatusGone, err)
		case errors.Is(err, errNotReproducible):
			writeErr(w, http.StatusUnprocessableEntity, err)
		default:
			writeErr(w, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, res)
}
