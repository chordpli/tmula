package load

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/engine"
	"github.com/chordpli/tmula/internal/safety"
)

// ThinkFunc yields the pause a virtual user takes between two steps of its
// session. It is called once per inter-step gap; returning a non-positive
// duration means "no pause". Keeping it a function (rather than a fixed range)
// lets the caller own the randomness source — the open-model scheduler draws a
// uniform think time from its seeded RNG, while the closed Run path passes nil.
type ThinkFunc func() time.Duration

// VirtualUser is one simulated principal: an identity, its credential, and any
// per-user template variables.
type VirtualUser struct {
	ID   string
	Cred domain.Credential
	Vars map[string]string
}

// StepResult records the outcome of one node visit by one virtual user.
type StepResult struct {
	UserID string
	NodeID domain.ID
	Resp   Response
	Err    error
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
	// maxConcurrency caps how many sessions Run drives at once; 0 means use the
	// maxConcurrentSessions default. It exists so tests can assert the fan-out is
	// actually bounded without spawning the full production-sized pool.
	maxConcurrency int
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
	r := &Runner{adapter: adapter, baseURL: baseURL, templates: templates}
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
			// Closed model: no think time, each user seeded by Seed+i so the
			// traversal is reproducible no matter which pool worker runs it.
			// runSession reuses the shared node→template map so it is resolved
			// exactly once for the whole run, and emits each result through emit.
			r.runSession(ctx, g, nodeTmpl, start, maxSteps, u, seed+int64(i), nil, emit)
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
		emit(StepResult{UserID: u.ID, Err: err})
		return
	}
	path, err := walker.Walk(start, maxSteps)
	if err != nil {
		emit(StepResult{UserID: u.ID, Err: err})
		return
	}

	sent := false
	var prevNode domain.ID // the node the user came from; "" at the entry
	for _, nodeID := range path {
		if ctx.Err() != nil {
			break // cancelled (kill switch): stop this user's journey
		}
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
			if d := think(); d > 0 && !sleep(ctx, d) {
				break
			}
		}
		req, err := Render(tmpl, r.baseURL, u.Cred, u.Vars)
		if err != nil {
			emit(StepResult{UserID: u.ID, NodeID: nodeID, Err: err})
			continue
		}
		resp, sErr := r.send(ctx, req)
		emit(StepResult{UserID: u.ID, NodeID: nodeID, Resp: resp, Err: sErr})
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
