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
	adapter   Adapter
	baseURL   string
	templates map[domain.ID]domain.APITemplate // keyed by APITemplate ID
	guard     *safety.Guard                    // optional; nil = no enforcement
	eventSink EventSink                        // optional; nil = no per-step events
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

// Run executes every virtual user as its own goroutine. Each user walks the
// graph from start and calls the API bound to each visited node. The run stops
// promptly when ctx is cancelled (the kill switch path). It returns every step
// result; failures are recorded per step rather than aborting the run.
func (r *Runner) Run(ctx context.Context, g domain.ScenarioGraph, start domain.ID, maxSteps int, users []VirtualUser, seed int64) ([]StepResult, error) {
	nodeTmpl, err := r.resolveNodeTemplates(g)
	if err != nil {
		return nil, err
	}

	var (
		mu      sync.Mutex
		results []StepResult
		wg      sync.WaitGroup
	)

	for i := range users {
		wg.Add(1)
		go func(i int, u VirtualUser) {
			defer wg.Done()
			// Closed model: no think time, each user seeded by Seed+i so the
			// traversal is reproducible. runSession reuses the shared node→template
			// map so it is resolved exactly once for the whole run.
			sr := r.runSession(ctx, g, nodeTmpl, start, maxSteps, u, seed+int64(i), nil)
			mu.Lock()
			results = append(results, sr...)
			mu.Unlock()
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
	return r.runSession(ctx, g, nodeTmpl, start, maxSteps, user, seed, think), nil
}

// runSession is the shared session body used by both Run (fanned out per user)
// and RunSession (one arrival). nodeTmpl is the already-resolved node→template
// map, so callers driving many sessions resolve templates once rather than per
// session.
func (r *Runner) runSession(ctx context.Context, g domain.ScenarioGraph, nodeTmpl map[domain.ID]domain.APITemplate, start domain.ID, maxSteps int, u VirtualUser, seed int64, think ThinkFunc) []StepResult {
	walker, err := engine.NewWalker(g, seed)
	if err != nil {
		return []StepResult{{UserID: u.ID, Err: err}}
	}
	path, err := walker.Walk(start, maxSteps)
	if err != nil {
		return []StepResult{{UserID: u.ID, Err: err}}
	}

	var results []StepResult
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
			results = append(results, StepResult{UserID: u.ID, NodeID: nodeID, Err: err})
			continue
		}
		resp, sErr := r.send(ctx, req)
		results = append(results, StepResult{UserID: u.ID, NodeID: nodeID, Resp: resp, Err: sErr})
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
	return results
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
