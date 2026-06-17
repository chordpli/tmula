package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/chordpli/tmula/server/internal/auth"
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
	// errCredentialSourceUnavailable: a distributed-auth finding's run carried a
	// reference-only credential source (the workers resolved it locally), but the
	// source is not resolvable server-side at reproduce time — the file is gone or
	// the env var is unset. The replay cannot rebuild the principal the shard ran
	// as, so it is refused with a typed sentinel (a 410-class "gone", parallel to
	// errSpecUnavailable) rather than replaying under the wrong — or no — user.
	errCredentialSourceUnavailable = errors.New("credential source no longer available for reproduce")
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
	user, err := s.sessionUser(ctx, spec, sess, guard)
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
//
// LOAD-BEARING INVARIANT (post-P3 distributed-auth split): a distributed run is
// authenticated ONLY from a reference-only credential SOURCE that the workers
// resolved locally by GLOBAL index (runspec.validateCredentialPool rejects inline
// secrets, bootstrap and login with workers). The reproduce path therefore
// rebuilds that SAME source-backed provider here and re-acquires the principal by
// the session's global index — the identical pure Acquire the shards used — so a
// distributed-auth finding replays under the exact principal it ran as. A
// distributed run with NO source pool stays unauthenticated, so the replay runs
// as the same anonymous user. If the source is no longer resolvable server-side
// (file gone, env unset), sessionUser returns errCredentialSourceUnavailable (a
// 410-class typed sentinel) rather than replaying under the wrong — or no — user.
// See runspec.validateCredentialPool and shardSpecFor (orchestrator.go) for the
// other halves of this contract; PR3 and PR4 land together (D4).
//
// LOGIN (CredLogin) REPRODUCE IS REFRESH-FREE: a login finding is replayed under a
// re-acquired token (one deterministic login of the same index), seeded onto Cred
// with NO holder and NO refresh closure. The replay therefore never performs a
// live mid-run refresh — which would make the verdict depend on token timing — so
// the reproduce stays deterministic about WHAT the session sent, exactly like the
// static-pool path. See login.go (loginAuthFor / loginAuth.seed wire the refresh
// only on the RUN path, never here).
func (s *Server) sessionUser(ctx context.Context, spec RunSpec, sess domain.EvidenceSession, guard *safety.Guard) (load.VirtualUser, error) {
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

	// Closed users are keyed by pool index; open arrivals are 1-based, so the
	// scheduler keyed them by arrival-1. The same offset arithmetic both run paths
	// use, so the replay re-acquires the SAME principal the evidence session ran as.
	idx := int(sess.UserIndex)
	if spec.IsOpen() && idx > 0 {
		idx--
	}

	// Login strategy: re-acquire the token deterministically and statically — a
	// refresh-FREE variant. The reproduce user gets the minted credential on Cred
	// and NO holder/refresh, so the replay never performs a live mid-run refresh
	// (which would make the verdict non-deterministic). The mint itself is a single
	// login of the same principal, keyed by the same index the run used.
	if spec.CredentialPool != nil && spec.CredentialPool.Strategy == domain.CredLogin {
		login, err := s.loginAuthFor(spec, guard)
		if err != nil {
			return load.VirtualUser{}, err
		}
		cred, err := login.provider.Acquire(ctx, login.cacheKey(idx))
		if err != nil {
			return load.VirtualUser{}, fmt.Errorf("api: re-acquire login token for reproduce: %w", err)
		}
		user.Cred = cred
		return user, nil
	}

	// Bootstrap-signup replay: re-acquire the principal deterministically by
	// re-running the signup ONCE for the same index — a refresh-FREE variant, like
	// login. Identity is a pure function of (runID, index) via the seeded signup
	// walk, so the replay provisions/recovers the same account the evidence session
	// ran as. The reproduce user gets the credential on Cred and NO holder (bootstrap
	// credentials never refresh mid-run anyway). A kept-accounts run re-acquires the
	// still-live account; a torn-down run re-provisions a fresh one under the same
	// deterministic identity — either way the replay runs as the same principal the
	// run keyed by this index. No teardown is wired here: the reproduce re-acquire
	// must not deprovision the account it (or the run, under keep-accounts) relies on.
	if spec.CredentialPool != nil && spec.CredentialPool.Strategy == domain.CredBootstrapSignup {
		boot, err := s.bootstrapAuthFor(spec, guard)
		if err != nil {
			return load.VirtualUser{}, err
		}
		// Reproduce only re-acquires; it must never deprovision the account it (or a
		// keep-accounts run) depends on, so strip any teardown wired by the builder.
		boot.provider.SetTeardown(nil)
		cred, err := boot.provider.Acquire(ctx, idx)
		if err != nil {
			return load.VirtualUser{}, fmt.Errorf("api: re-acquire bootstrap account for reproduce: %w", err)
		}
		user.Cred = cred
		return user, nil
	}

	// Distributed-auth (source pool) replay: the run authenticated by shipping a
	// reference each worker resolved locally, so rebuild that SAME source-backed
	// provider and re-acquire the principal by the session's GLOBAL index — the
	// identical pure Acquire the shards used. A still-resolvable source replays the
	// exact principal; an unresolvable one (file gone, env unset) is refused with a
	// typed sentinel rather than replaying under the wrong user.
	if spec.CredentialPool != nil && spec.CredentialPool.Source != nil {
		provider, err := s.sourceProviderFor(ctx, *spec.CredentialPool.Source)
		if err != nil {
			return load.VirtualUser{}, err
		}
		cred, err := provider.Acquire(ctx, idx)
		if err != nil {
			return load.VirtualUser{}, fmt.Errorf("api: acquire credential for reproduce: %w", err)
		}
		user.Cred = cred
		return user, nil
	}

	provider, err := spec.CredentialProvider()
	if err != nil {
		return load.VirtualUser{}, err
	}
	if provider != nil {
		cred, err := provider.Acquire(ctx, idx)
		if err != nil {
			return load.VirtualUser{}, fmt.Errorf("api: acquire credential for reproduce: %w", err)
		}
		user.Cred = cred
	}
	return user, nil
}

// sourceProviderFor rebuilds the index-deterministic PoolProvider behind a
// distributed-auth run's reference-only credential source, resolving it
// server-side at reproduce time. The root is the engine's working directory —
// the same operator-asserted location the workers read — so the reproduce
// principal matches the shard's. A source that no longer resolves (file removed,
// env unset) yields errCredentialSourceUnavailable, a 410-class typed sentinel,
// so the handler reports "gone" instead of replaying under the wrong principal.
func (s *Server) sourceProviderFor(ctx context.Context, ref domain.CredentialSourceRef) (auth.Provider, error) {
	src, err := auth.SourceFromRef(ref, s.credentialRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCredentialSourceUnavailable, err)
	}
	entries, err := src.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCredentialSourceUnavailable, err)
	}
	provider, err := auth.NewPoolProvider(entries)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCredentialSourceUnavailable, err)
	}
	return provider, nil
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
//
// The store path is a read-modify-write: Findings → mutate → SaveFindings.
// Two concurrent reproduce calls for different findings of the same run would
// each read the full list, stamp only their own finding, and the later
// SaveFindings would overwrite the earlier one — silently losing the first
// RootCauseClass in the system of record. s.annotateMu serializes this
// critical section so both updates are visible regardless of ordering.
func (s *Server) annotateRootCause(runID domain.ID, cat domain.FindingCategory, ref, class string) {
	// Update the live in-memory copy first (outside the annotation lock — it is
	// guarded by rs.mu, an independent per-run lock, so it does not need to be
	// inside the store critical section).
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
	// Serialize the store read-modify-write so concurrent reproduce calls for
	// different findings of the same run cannot produce a lost update (see doc
	// comment above).
	s.annotateMu.Lock()
	defer s.annotateMu.Unlock()
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
		case errors.Is(err, errSpecUnavailable), errors.Is(err, errCredentialSourceUnavailable):
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
