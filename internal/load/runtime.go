package load

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/engine"
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
}

// NewRunner builds a Runner. templates is keyed by APITemplate ID; a node with
// an empty or unknown APITemplateID is treated as a pure state (no request).
func NewRunner(adapter Adapter, baseURL string, templates map[domain.ID]domain.APITemplate) *Runner {
	return &Runner{adapter: adapter, baseURL: baseURL, templates: templates}
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
	for _, nodeID := range path {
		if ctx.Err() != nil {
			break // cancelled (kill switch): stop this user's journey
		}
		tmpl, ok := nodeTmpl[nodeID]
		if !ok {
			continue // pure state node, no request
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
		resp, sErr := r.adapter.Send(ctx, req)
		results = append(results, StepResult{UserID: u.ID, NodeID: nodeID, Resp: resp, Err: sErr})
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
