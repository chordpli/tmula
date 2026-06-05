package load

import (
	"context"
	"fmt"
	"sync"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/engine"
)

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
	record := func(sr StepResult) {
		mu.Lock()
		results = append(results, sr)
		mu.Unlock()
	}

	for i := range users {
		wg.Add(1)
		go func(i int, u VirtualUser) {
			defer wg.Done()

			walker, err := engine.NewWalker(g, seed+int64(i))
			if err != nil {
				record(StepResult{UserID: u.ID, Err: err})
				return
			}
			path, err := walker.Walk(start, maxSteps)
			if err != nil {
				record(StepResult{UserID: u.ID, Err: err})
				return
			}
			for _, nodeID := range path {
				if ctx.Err() != nil {
					return // cancelled (kill switch)
				}
				tmpl, ok := nodeTmpl[nodeID]
				if !ok {
					continue // pure state node, no request
				}
				req, err := Render(tmpl, r.baseURL, u.Cred, u.Vars)
				if err != nil {
					record(StepResult{UserID: u.ID, NodeID: nodeID, Err: err})
					continue
				}
				resp, sErr := r.adapter.Send(ctx, req)
				record(StepResult{UserID: u.ID, NodeID: nodeID, Resp: resp, Err: sErr})
			}
		}(i, users[i])
	}

	wg.Wait()
	return results, nil
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
