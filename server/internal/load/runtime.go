package load

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/engine"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/chordpli/tmula/server/internal/safety"
)

// ThinkFunc yields the pause a virtual user takes between two steps of its
// session. It is called once per inter-step gap; returning a non-positive
// duration means "no pause". Keeping it a function (rather than a fixed range)
// lets the caller own the randomness source — the open-model scheduler draws a
// uniform think time from its seeded RNG, while the closed Run path derives one
// per user from the configured think time (WithThinkTime; nil when unset).
type ThinkFunc func() time.Duration

// VirtualUser is one simulated principal: an identity, its credential, and any
// per-user template variables.
type VirtualUser struct {
	ID   string
	Cred domain.Credential
	Vars map[string]string
	// Holder, when non-nil, is the live credential box the session reads its
	// credential from per step instead of the static Cred — the seam the
	// login/refresh path uses to rotate a token mid-run. It is nil for every
	// existing (static-pool, bootstrap, unauthenticated) run, and on that path
	// runSession renders Cred exactly as before and never touches a holder (no
	// lock). It is a CredentialHolder (interface over a pointer), so the shared
	// login scope can hand one holder to every user and a single refresh reaches
	// all of them. It is a runtime-only seam wired in-process by the orchestrator,
	// never serialized (json:"-"), so a spec's Users array marshals byte-for-byte
	// as before — the holder, like a minted token, never crosses the wire.
	Holder CredentialHolder `json:"-"`
	// Refresh, when non-nil, re-acquires this user's credential (re-runs the login
	// flow for the user's index) and rotates the Holder in place. runSession calls
	// it at most once per request on a 401, then retries the request once with the
	// refreshed credential. It is the orchestrator's job to bind the correct index
	// (the same Seed-offset the run path keys credentials by) into this closure, so
	// the runtime never re-derives an index. Nil on every non-login run, and on
	// that path no refresh is ever attempted. Like Holder it is a runtime-only seam
	// (json:"-").
	Refresh RefreshFunc `json:"-"`
}

// RefreshFunc re-acquires a virtual user's credential mid-run and rotates its
// holder in place, returning an error if the re-acquire failed. It is the seam the
// 401 recovery path drives: the orchestrator builds one per user, binding the
// user's index and login transport, so the runtime stays free of index arithmetic
// and the login transport.
type RefreshFunc func(ctx context.Context) error

// cred resolves the credential to render a step with: the live value from the
// holder when one is set (the login/refresh path), otherwise the static Cred
// (every existing run). Keeping the holder read here — and only when Holder!=nil —
// is what guarantees the static path takes zero holder locks and renders Cred
// byte-for-byte as before.
func (u VirtualUser) cred() domain.Credential {
	if u.Holder != nil {
		return u.Holder.Get()
	}
	return u.Cred
}

// StepResult records the outcome of one node visit by one virtual user.
type StepResult struct {
	UserID string
	NodeID domain.ID
	Resp   Response
	Err    error
	// Seed is the walk seed this session ran with, stamped on every result so
	// downstream evidence carries the reproduce coordinate without a side
	// channel (closed: run seed + pool index; open: run seed + arrival).
	Seed int64
	// Path is the node sequence the session had traversed up to and including
	// this step. It is attached only when the step FAILED (an error, or a
	// status >= 400) and is a reslice of the session's precomputed walk —
	// shared, never copied — so healthy traffic pays no per-step path cost.
	Path []domain.ID
	// ErrorClass, when non-empty, overrides the class the recording layer would
	// otherwise derive from Resp/Err. The runtime sets it to
	// obs.ErrorClassAuthRefresh for an exhausted-refresh 401 (the token expired
	// and re-acquiring it did not recover), so the recording orchestrator carries
	// that class through to the aggregator — which excuses it from the error rate.
	// Empty for every ordinary result, so the existing class derivation (transport
	// on a send error, empty otherwise) is unchanged.
	ErrorClass string
}

// Runner drives many virtual users concurrently through a scenario graph,
// calling the system under test through an adapter.
type Runner struct {
	adapter    Adapter
	baseURL    string
	templates  map[domain.ID]domain.APITemplate // keyed by APITemplate ID
	guard      *safety.Guard                    // optional; nil = no enforcement
	eventSink  EventSink                        // optional; nil = no per-step events
	resultSink ResultSink                       // optional; nil = accumulate and return
	runID      domain.ID
	scenarioID domain.ID
	// maxConcurrency caps how many sessions Run drives at once; 0 means use the
	// maxConcurrentSessions default. It exists so tests can assert the fan-out is
	// actually bounded without spawning the full production-sized pool.
	maxConcurrency int
	// deviation is the per-step probability (0..1) that a session departs from
	// the weighted happy path; 0 (the default) keeps the plain Walk. See
	// WithDeviation for how the single rate maps onto the engine policy.
	deviation float64
	// think paces the closed-model fan-out: Run pauses each user a uniform draw
	// in [MinMs, MaxMs] between consecutive requests. The zero value means no
	// pause (the historical closed behavior). It does not affect RunSession,
	// whose caller owns think time explicitly.
	think domain.ThinkTime
	// sleepFn is how a session pauses for think time; the package-level sleep
	// (a cancellable real timer) by default, overridable via withSleep so tests
	// can assert pacing against a virtual clock without real waiting.
	sleepFn func(context.Context, time.Duration) bool
}

// StepEvent is one step a virtual user took, emitted live for visualization.
// From is the node the user came from ("" at the entry); To is the node reached.
// OK is true on a non-error response below 400. Most events are a request (To
// made an API call); a Terminal event marks reaching a template-less terminal
// node (e.g. done/exit) where no request fires — it has no Status/LatencyMs and
// exists so the live view can show how many users ended there (completion vs
// drop-off).
type StepEvent struct {
	UserID    string
	From      string
	To        string
	Status    int
	LatencyMs float64
	OK        bool
	Terminal  bool
}

// EventSink receives a StepEvent for every request a session makes. It is called
// concurrently from many session goroutines, so it must be safe for concurrent
// use; it should also be cheap (it runs on the request hot path).
type EventSink func(StepEvent)

// ResultSink receives one StepResult as each request completes, the streaming
// counterpart to Run's returned slice. Setting one (WithResultSink) makes the
// Runner hand every result to the sink the moment it is produced and NOT retain
// the full []StepResult — so a worker can fold each result into a summary or push
// it onto a gRPC stream without ever buffering tens of millions of structs in
// RAM. Like EventSink it fires concurrently from many session goroutines, so it
// MUST be safe for concurrent use (every caller's sink guards its shared state
// with a mutex or atomic); it should also be cheap, as it runs on the request hot
// path.
type ResultSink func(StepResult)

// NewRunner builds a Runner. templates is keyed by APITemplate ID; a node with
// an empty or unknown APITemplateID is treated as a pure state (no request).
func NewRunner(adapter Adapter, baseURL string, templates map[domain.ID]domain.APITemplate, opts ...RunnerOption) *Runner {
	r := &Runner{adapter: adapter, baseURL: baseURL, templates: templates, sleepFn: sleep}
	for _, o := range opts {
		o(r)
	}
	return r
}

// RunnerOption customizes a Runner.
type RunnerOption func(*Runner)

// WithGuard enforces the safety policy on every request the runner sends: the
// host allowlist — checked against the *rendered* URL so a template variable
// cannot redirect traffic off the allowlisted target — the rate/concurrency cap
// (which throttles rather than drops), and the kill switch.
func WithGuard(g *safety.Guard) RunnerOption { return func(r *Runner) { r.guard = g } }

// WithEventSink streams a StepEvent for every request, so a caller (the control
// plane) can show live per-user traffic. Leave it unset for normal runs.
func WithEventSink(s EventSink) RunnerOption { return func(r *Runner) { r.eventSink = s } }

// WithResultSink streams every StepResult to s as it is produced instead of
// accumulating them; when set, Run feeds each result to the sink and returns an
// empty slice, so the caller never holds the whole run in memory (the path that
// scales to tens of millions of requests per worker). The sink fires from many
// session goroutines concurrently, so it must be safe for concurrent use — see
// ResultSink. Leave it unset to keep Run's slice-returning behavior for small
// runs and existing callers.
func WithResultSink(s ResultSink) RunnerOption { return func(r *Runner) { r.resultSink = s } }

// WithCorrelationIDs stamps every outbound request with run/scenario metadata.
// Per-request node and session ids are filled in by runSession.
func WithCorrelationIDs(runID, scenarioID domain.ID) RunnerOption {
	return func(r *Runner) {
		r.runID = runID
		r.scenarioID = scenarioID
	}
}

// WithDeviation injects probabilistic deviation into every session's walk: with
// probability rate per step the virtual user departs from the weighted happy
// path. The single rate maps onto engine.DeviationPolicy{Rate: rate, Abandon:
// true, Explore: true}: both failure modes are enabled because real users both
// leave mid-flow and wander onto unlikely screens, and with both set the engine
// splits each deviation 50:50 between abandoning and exploring (see
// engine/deviation.go), so one knob yields a balanced mix. Dependency edges are
// never violated either way. Rate 0 (the default) keeps the plain weighted Walk,
// so an undeviated run is byte-for-byte what it always was.
func WithDeviation(rate float64) RunnerOption { return func(r *Runner) { r.deviation = rate } }

// WithThinkTime paces the closed-model fan-out: every user driven by Run pauses
// a uniform draw in [MinMs, MaxMs] between consecutive requests, drawn from a
// per-user RNG derived from the user's walk seed so pacing is as reproducible
// as the traversal. The zero value means no pause — the historical closed
// behavior — so existing callers are unaffected. RunSession ignores it: its
// caller (the open-model scheduler) owns think time explicitly via the think
// argument.
func WithThinkTime(tt domain.ThinkTime) RunnerOption { return func(r *Runner) { r.think = tt } }

// withSleep overrides how a session pauses for think time. It is unexported
// because production always wants the real cancellable timer; tests inject a
// virtual clock so think pacing is asserted without real waiting.
func withSleep(fn func(context.Context, time.Duration) bool) RunnerOption {
	return func(r *Runner) { r.sleepFn = fn }
}

// withMaxConcurrency overrides the session fan-out cap (default
// maxConcurrentSessions). It is unexported because production always wants the
// tuned default; tests use it to bound the pool small enough to assert the cap
// holds without launching thousands of goroutines.
func withMaxConcurrency(n int) RunnerOption { return func(r *Runner) { r.maxConcurrency = n } }

// throttleInterval is how long a session waits before retrying a reservation the
// rate/concurrency cap is currently refusing.
const throttleInterval = 5 * time.Millisecond

// send dispatches one request through the adapter, enforcing the safety guard
// when present. A guard rejection (off-allowlist host, or the kill switch) is
// returned as the step error rather than reaching the target.
func (r *Runner) send(ctx context.Context, req RenderedRequest) (Response, error) {
	if r.guard == nil {
		return r.adapter.Send(ctx, req)
	}
	if err := r.guard.AllowHost(req.URL); err != nil {
		return Response{}, err
	}
	if err := r.reserve(ctx); err != nil {
		return Response{}, err
	}
	defer r.guard.Release()
	resp, err := r.adapter.Send(ctx, req)
	r.guard.ReportOutcome(err == nil && resp.StatusCode < 500)
	return resp, err
}

// reserve blocks until the guard grants a rate token and a concurrency slot, so
// the cap throttles offered load rather than dropping it. It returns on the kill
// switch (a non-limit error) or when ctx is cancelled.
func (r *Runner) reserve(ctx context.Context) error {
	for {
		err := r.guard.Reserve()
		if err == nil {
			return nil
		}
		var le *safety.LimitError
		if !errors.As(err, &le) {
			return err // killed, not a transient cap rejection
		}
		if !sleep(ctx, throttleInterval) {
			return ctx.Err()
		}
	}
}

// maxConcurrentSessions bounds how many virtual-user sessions Run drives at once.
// Run used to spawn one goroutine per user with no cap, so a closed pool of a few
// hundred thousand users (the ~270k-per-node ceiling) meant a few hundred thousand
// live goroutines and their stacks at once. A fixed worker pool of this size keeps
// the goroutine (and stack) footprint flat regardless of pool size while still
// running every user; a pool smaller than this just uses one goroutine per user.
// It is sized to comfortably saturate the request path without the scheduler and
// memory cost of an unbounded fan-out.
const maxConcurrentSessions = 4096

// Run executes the virtual users through a bounded worker pool: at most
// maxConcurrentSessions sessions run concurrently rather than one goroutine per
// user, so a huge closed pool no longer spawns a goroutine (and stack) per user.
// Each user walks the graph from start and calls the API bound to each visited
// node. The run stops promptly when ctx is cancelled (the kill switch path).
//
// Determinism is preserved exactly: user i is still seeded with seed+i regardless
// of which pool worker happens to run it, so the bound changes only the
// concurrency, never the per-user traversal.
//
// By default Run returns every step result (failures are recorded per step rather
// than aborting the run). When a ResultSink is configured (WithResultSink) each
// result is instead handed to the sink as it completes and Run returns an empty
// slice, so a caller folding millions of requests never holds them all in memory.
func (r *Runner) Run(ctx context.Context, g domain.ScenarioGraph, start domain.ID, maxSteps int, users []VirtualUser, seed int64) ([]StepResult, error) {
	nodeTmpl, err := r.resolveNodeTemplates(g)
	if err != nil {
		return nil, err
	}

	// emit routes each result either to the configured sink (streaming, no
	// retention) or into the accumulated slice under mu. It is shared by every pool
	// worker, so the slice path guards with mu and the sink path relies on the
	// sink's own documented concurrency-safety.
	var (
		mu      sync.Mutex
		results []StepResult
	)
	emit := r.resultSink
	if emit == nil {
		emit = func(sr StepResult) {
			mu.Lock()
			results = append(results, sr)
			mu.Unlock()
		}
	}

	// Bound the fan-out with a counting semaphore: a buffered channel sized to the
	// concurrency target (capped at the user count so a small pool is not
	// over-provisioned). Acquiring a slot before launching a session and releasing
	// it on completion caps live session goroutines at the buffer size while still
	// scheduling every user. No new dependency — the channel is the semaphore.
	limit := r.maxConcurrency
	if limit <= 0 {
		limit = maxConcurrentSessions
	}
	if len(users) < limit {
		limit = len(users)
	}
	sem := make(chan struct{}, limit)

	var wg sync.WaitGroup
	for i := range users {
		// Stop launching new sessions once cancelled (kill switch); in-flight
		// sessions observe ctx themselves and unwind promptly.
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{} // blocks once limit sessions are in flight
		wg.Add(1)
		go func(i int, u VirtualUser) {
			defer wg.Done()
			defer func() { <-sem }()
			// Closed model: each user is seeded by Seed+i so the traversal — and
			// the think pacing derived from the same seed — is reproducible no
			// matter which pool worker runs it. thinkFor is nil unless a think
			// time was configured (WithThinkTime), keeping the historical
			// no-pause hammering as the default. runSession reuses the shared
			// node→template map so it is resolved exactly once for the whole
			// run, and emits each result through emit.
			userSeed := seed + int64(i)
			r.runSession(ctx, g, nodeTmpl, start, maxSteps, u, userSeed, r.thinkFor(userSeed), emit)
		}(i, users[i])
	}

	wg.Wait()
	return results, nil
}

// RunSession drives a single virtual user through one journey: it walks the
// graph from start (seeded by seed for reproducibility), and for every visited
// node calls the API bound to it, pausing for think() between consecutive steps
// when think is non-nil. It is the per-arrival unit the open-model workload
// scheduler launches, and the per-user unit Run fans out — so the walk → render
// → send → record logic lives in exactly one place.
//
// The returned slice holds one StepResult per request (and a single error
// StepResult if the walk itself fails); per-request failures are recorded rather
// than aborting the session. The session stops promptly when ctx is cancelled.
func (r *Runner) RunSession(ctx context.Context, g domain.ScenarioGraph, start domain.ID, maxSteps int, user VirtualUser, seed int64, think ThinkFunc) ([]StepResult, error) {
	nodeTmpl, err := r.resolveNodeTemplates(g)
	if err != nil {
		return nil, err
	}
	// One arrival's results are bounded by its own walk length, so RunSession
	// always materializes them for its caller (the open-model scheduler) via a
	// local accumulator — independent of any Runner-wide ResultSink, which targets
	// the closed Run fan-out.
	var results []StepResult
	r.runSession(ctx, g, nodeTmpl, start, maxSteps, user, seed, think, func(sr StepResult) {
		results = append(results, sr)
	})
	return results, nil
}

// runSession is the shared session body used by both Run (fanned out per user)
// and RunSession (one arrival). nodeTmpl is the already-resolved node→template
// map, so callers driving many sessions resolve templates once rather than per
// session. Each produced StepResult is handed to emit the moment it is known
// (including the single error result when the walk itself fails) rather than
// returned, so the caller chooses whether to accumulate them or stream them to a
// sink. emit must not be nil; for one session it is called only from this
// goroutine, but Run's shared emit is invoked from many sessions at once, so that
// emit is responsible for its own concurrency-safety.
func (r *Runner) runSession(ctx context.Context, g domain.ScenarioGraph, nodeTmpl map[domain.ID]domain.APITemplate, start domain.ID, maxSteps int, u VirtualUser, seed int64, think ThinkFunc, emit func(StepResult)) {
	walker, err := engine.NewWalker(g, seed)
	if err != nil {
		emit(StepResult{UserID: u.ID, Err: err, Seed: seed})
		return
	}
	// Deviation is injected at the walk. A configured rate takes WalkWithDeviation
	// with Abandon and Explore both enabled — real users both leave mid-flow and
	// wander onto unlikely screens, and with both set the engine splits each
	// deviation 50:50 between the two (engine/deviation.go) — so the one rate
	// yields a balanced mix while dependency edges stay inviolable. Rate 0 takes
	// the plain Walk, so an undeviated session draws exactly the random sequence
	// it always did.
	var path []domain.ID
	if r.deviation > 0 {
		path, err = walker.WalkWithDeviation(start, maxSteps, engine.DeviationPolicy{
			Rate:    r.deviation,
			Abandon: true,
			Explore: true,
		})
	} else {
		path, err = walker.Walk(start, maxSteps)
	}
	if err != nil {
		emit(StepResult{UserID: u.ID, Err: err, Seed: seed})
		return
	}

	sent := false
	var prevNode domain.ID // the node the user came from; "" at the entry
	sessionVars := copyVars(u.Vars)
	scenarioID := r.scenarioID
	if scenarioID == "" {
		scenarioID = g.ID
	}
	for stepIdx, nodeID := range path {
		if ctx.Err() != nil {
			break // cancelled (kill switch): stop this user's journey
		}
		// The journey up to and including this node — a reslice of the
		// precomputed (immutable) walk, attached to failed results only.
		walked := path[:stepIdx+1]
		from := prevNode
		prevNode = nodeID // advance even for pure-state nodes, so edges are correct
		tmpl, ok := nodeTmpl[nodeID]
		if !ok {
			// Pure-state node (a terminal like done/exit): no request fires, but
			// emit a terminal transition so the live view can show how many users
			// ended here (completion vs drop-off). Skip the synthetic entry hop
			// (from == "") since "ending" only makes sense after a real step.
			if r.eventSink != nil && from != "" {
				r.eventSink(StepEvent{UserID: u.ID, From: string(from), To: string(nodeID), OK: true, Terminal: true})
			}
			continue
		}
		// Think time is the pause a real user takes between actions; apply it
		// before each request after the first, and make it cancellable so the
		// kill switch is not blocked by a pending pause.
		if sent && think != nil {
			if d := think(); d > 0 && !r.sleepFn(ctx, d) {
				break
			}
		}
		// Resolve the credential per step: u.cred() reads the live value from a
		// holder when the user carries one (the login/refresh path, so a mid-run
		// token rotation is visible on the next request), and otherwise returns the
		// static u.Cred without touching any lock — the unchanged static path.
		req, err := Render(tmpl, r.baseURL, u.cred(), sessionVars)
		if err != nil {
			emit(StepResult{UserID: u.ID, NodeID: nodeID, Err: err, Seed: seed, Path: walked})
			continue
		}
		req.Correlation = RequestCorrelation{
			RunID:      r.runID,
			ScenarioID: scenarioID,
			NodeID:     nodeID,
			SessionID:  u.ID,
		}
		resp, sErr := r.send(ctx, req)
		// Mid-run auth recovery: a 401 with a refresher attached re-acquires the
		// token and retries the request exactly ONCE. On a successful retry the
		// original 401 is swallowed — only the retry is emitted, so a recovered
		// session contributes exactly one (healthy) observation. When the refresh or
		// the retry does not recover, the 401 is emitted ONCE carrying the
		// auth-refresh class, which obs.failed() excuses from the error rate (expired
		// auth is not a target defect). Reactive 401-only by design (no TTL/clock).
		var authRefreshClass string
		if sErr == nil && resp.StatusCode == http.StatusUnauthorized && u.Refresh != nil {
			if err := u.Refresh(ctx); err != nil {
				// Re-acquire failed (login endpoint down, etc.): keep the original 401
				// but mark it auth-refresh so it does not inflate the error rate, and
				// do not retry.
				authRefreshClass = obs.ErrorClassAuthRefresh
			} else {
				// Re-render with the rotated credential and retry once.
				retryReq, rErr := Render(tmpl, r.baseURL, u.cred(), sessionVars)
				if rErr != nil {
					sErr = rErr
				} else {
					retryReq.Correlation = req.Correlation
					resp, sErr = r.send(ctx, retryReq)
					if sErr == nil && resp.StatusCode == http.StatusUnauthorized {
						// Refresh exhausted: the fresh token still 401s. Tag it so it is
						// excused, rather than counted as a contract/threshold failure.
						authRefreshClass = obs.ErrorClassAuthRefresh
					}
				}
			}
		}
		if sErr == nil && len(tmpl.Extract) > 0 {
			extracted, err := ExtractVariables(resp.Body, tmpl.Extract)
			if err != nil {
				sErr = err
			} else {
				for k, v := range extracted {
					sessionVars[k] = v
				}
			}
		}
		sr := StepResult{UserID: u.ID, NodeID: nodeID, Resp: resp, Err: sErr, Seed: seed, ErrorClass: authRefreshClass}
		if sErr != nil || resp.StatusCode >= 400 {
			sr.Path = walked // failed step: carry the journey for evidence
		}
		emit(sr)
		if r.eventSink != nil {
			r.eventSink(StepEvent{
				UserID:    u.ID,
				From:      string(from),
				To:        string(nodeID),
				Status:    resp.StatusCode,
				LatencyMs: resp.LatencyMs,
				OK:        sErr == nil && resp.StatusCode < 400,
			})
		}
		sent = true
	}
}

// thinkFor builds one closed-model user's think pacer from the Runner's
// configured think time: a uniform draw in [MinMs, MaxMs] from a user-local RNG
// derived from the user's walk seed, so pacing is reproducible and never shared
// across session goroutines. A zero range yields nil — no pause — which keeps
// the historical closed behavior (and test runtime) unless a think time was
// explicitly configured. The seed is XOR-offset so think draws do not correlate
// with traversal choices, mirroring the open scheduler's thinkFunc.
func (r *Runner) thinkFor(seed int64) ThinkFunc {
	if r.think.MaxMs <= 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(seed ^ 0x5DEECE66D))
	span := r.think.MaxMs - r.think.MinMs
	return func() time.Duration {
		ms := r.think.MinMs
		if span > 0 {
			ms += rng.Intn(span + 1)
		}
		return time.Duration(ms) * time.Millisecond
	}
}

func copyVars(vars map[string]string) map[string]string {
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}

// sleep pauses for d or until ctx is done, whichever comes first. It reports
// true if the full duration elapsed and false if ctx was cancelled, so callers
// can stop a session promptly on the kill-switch path. The timer is always
// stopped to avoid leaking it when ctx wins the race.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// resolveNodeTemplates maps each node to its API template (if any).
func (r *Runner) resolveNodeTemplates(g domain.ScenarioGraph) (map[domain.ID]domain.APITemplate, error) {
	out := make(map[domain.ID]domain.APITemplate, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.APITemplateID == "" {
			continue
		}
		tmpl, ok := r.templates[n.APITemplateID]
		if !ok {
			return nil, fmt.Errorf("load: node %q references unknown api template %q", n.ID, n.APITemplateID)
		}
		out[n.ID] = tmpl
	}
	return out, nil
}
