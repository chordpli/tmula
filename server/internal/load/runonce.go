package load

import (
	"context"
	"fmt"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/engine"
)

// RunOnce walks a flow exactly once and returns the variables it captured via
// each step's Extract, WITHOUT ever touching the runner's result or event sink.
// It is the findings-isolated setup primitive: the login (and, later, signup)
// transport compiles a standalone flow ABOVE the runner and walks it here to mint
// a token, and because RunOnce produces no StepResult/StepEvent the minted traffic
// never appears in the run's observations or findings. It still routes every
// request through the same send path (so the safety guard's allowlist and rate cap
// apply), renders with the seed user's credential and vars, and threads each step's
// extracted variables into the next step's render context exactly like a normal
// session — so a multi-step login (e.g. fetch a CSRF token, then POST it) works.
//
// A non-2xx status or a transport/extract error on any step is returned as an
// error rather than swallowed, so the caller can fail loudly instead of
// authenticating as nobody. nodeTmpl is passed in already resolved (the caller
// resolves it once and reuses it across re-mints); pass the result of
// resolveNodeTemplates for the flow.
func (r *Runner) RunOnce(ctx context.Context, g domain.ScenarioGraph, nodeTmpl map[domain.ID]domain.APITemplate, start domain.ID, maxSteps int, u VirtualUser, seed int64) (map[string]string, error) {
	walker, err := engine.NewWalker(g, seed)
	if err != nil {
		return nil, fmt.Errorf("load: runOnce build walker: %w", err)
	}
	path, err := walker.Walk(start, maxSteps)
	if err != nil {
		return nil, fmt.Errorf("load: runOnce walk: %w", err)
	}

	vars := copyVars(u.Vars)
	for _, nodeID := range path {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		tmpl, ok := nodeTmpl[nodeID]
		if !ok {
			continue // pure-state node: no request
		}
		req, err := Render(tmpl, r.baseURL, u.Cred, vars)
		if err != nil {
			return nil, fmt.Errorf("load: runOnce render node %q: %w", nodeID, err)
		}
		resp, err := r.send(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("load: runOnce send node %q: %w", nodeID, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("load: runOnce node %q returned status %d (login flow step must succeed)", nodeID, resp.StatusCode)
		}
		if len(tmpl.Extract) > 0 {
			extracted, err := ExtractVariables(resp.Body, tmpl.Extract)
			if err != nil {
				return nil, fmt.Errorf("load: runOnce extract node %q: %w", nodeID, err)
			}
			for k, v := range extracted {
				vars[k] = v
			}
		}
	}
	return vars, nil
}

// ResolveNodeTemplates exposes the runner's node→template resolution so a caller
// driving RunOnce can resolve a flow's templates once (and reuse the map across
// re-mints) rather than re-resolving per login. It mirrors what Run/RunSession do
// internally before walking.
func (r *Runner) ResolveNodeTemplates(g domain.ScenarioGraph) (map[domain.ID]domain.APITemplate, error) {
	return r.resolveNodeTemplates(g)
}
