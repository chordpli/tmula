package api

import (
	"context"
	"fmt"
	"sync"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/safety"
)

// bootstrapMaxConcurrency caps the prewarm provisioning burst regardless of how
// generous the run's rate cap is, so even a high-concurrency run does not slam a
// fragile signup endpoint with thousands of simultaneous registrations. The
// effective prewarm concurrency is min(this, RateCap.MaxConcurrency).
const bootstrapMaxConcurrency = 16

// bootstrapAuth bundles the runtime pieces a bootstrap-signup run is driven by: the
// signup provider (cache-by-index + in-flight dedup + Prewarm) and the
// effective prewarm concurrency. It is built once per run, above the load runner,
// from the spec's compiled signup flow. A later PR adds the teardown half.
type bootstrapAuth struct {
	provider *auth.BootstrapSignupProvider
	// prewarmConcurrency bounds the provisioning burst: min(RateCap.MaxConcurrency,
	// bootstrapMaxConcurrency), always at least 1.
	prewarmConcurrency int
}

// bootstrapAuthFor builds the bootstrap-signup provider for a CredBootstrapSignup
// run by compiling the pool's declarative SignupFlow into a signup transport and
// wrapping it in a BootstrapSignupProvider. It returns (nil, nil) for any
// non-bootstrap pool, so callers can branch on it. The signup runner is guarded by
// the run's safety policy so the signup endpoint obeys the same allowlist and rate
// cap as the simulated traffic, and carries no result/event sink so the
// provisioning traffic stays findings-isolated.
func (s *Server) bootstrapAuthFor(spec RunSpec, guard *safety.Guard) (*bootstrapAuth, error) {
	if spec.CredentialPool == nil || spec.CredentialPool.Strategy != domain.CredBootstrapSignup {
		return nil, nil
	}
	if spec.CredentialPool.SignupFlow == nil {
		// Validate rejects this on the runnable path; guard against a programming
		// error reaching the runtime with no flow to provision from.
		return nil, fmt.Errorf("api: bootstrap run has no signup flow to provision from")
	}
	flow, err := compileSignupFlow(*spec.CredentialPool.SignupFlow)
	if err != nil {
		return nil, fmt.Errorf("api: compile signup flow: %w", err)
	}
	// A dedicated runner for the signup flow: same adapter and base URL as the run,
	// guarded so the signup endpoint is allowlist-checked and rate-capped. It carries
	// no result/event sink, so RunOnce (which the transport drives) stays findings-
	// isolated even if those were set.
	runner := load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, flow.Templates, load.WithGuard(guard))
	signup, err := load.NewSignupRunner(runner, flow, spec.Seed)
	if err != nil {
		return nil, fmt.Errorf("api: compile signup flow: %w", err)
	}
	provider, err := auth.NewBootstrapSignupProvider(auth.SignupFunc(signup))
	if err != nil {
		return nil, fmt.Errorf("api: build bootstrap provider: %w", err)
	}
	return &bootstrapAuth{
		provider:           provider,
		prewarmConcurrency: effectivePrewarmConcurrency(spec.TargetEnv.RateCap.MaxConcurrency),
	}, nil
}

// effectivePrewarmConcurrency resolves the provisioning burst width:
// min(RateCap.MaxConcurrency, bootstrapMaxConcurrency), floored at 1 so a zero/
// negative cap still makes progress (sequentially).
func effectivePrewarmConcurrency(rateCapMaxConcurrency int) int {
	c := rateCapMaxConcurrency
	if c > bootstrapMaxConcurrency {
		c = bootstrapMaxConcurrency
	}
	if c < 1 {
		c = 1
	}
	return c
}

// Prewarm provisions accounts for indices [0,n) ahead of the run, bounded to the
// effective prewarm concurrency so the burst respects the run's rate guard and the
// bootstrap-specific cap. Each provision goes through the deduping provider, so a
// concurrent prewarm still signs up each index exactly once. The first error aborts
// (a failed provision must fail the run, not run it half-authenticated); a canceled
// context stops the burst.
func (b *bootstrapAuth) Prewarm(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	sem := make(chan struct{}, b.prewarmConcurrency)
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			i = n // stop scheduling
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := b.provider.Acquire(ctx, idx); err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel() // stop the rest of the burst on the first failure
				})
			}
		}(i)
	}
	wg.Wait()
	return firstErr
}

// compileSignupFlow turns a declarative domain.SignupFlow into a runnable
// load.SignupFlow: it compiles the signup steps into a graph + templates (the same
// compile-high/run-low direction the main run path uses) and carries the token/
// subject capture variable names. It compiles only the SIGNUP journey; the teardown
// journey is compiled separately by the teardown PR.
func compileSignupFlow(flow domain.SignupFlow) (load.SignupFlow, error) {
	graph, templates, err := compileSignupSteps(flow.Steps, "signup")
	if err != nil {
		return load.SignupFlow{}, err
	}
	start := flow.Start
	if start == "" && len(flow.Steps) > 0 {
		start = flow.Steps[0].ID
	}
	return load.SignupFlow{
		Graph:      graph,
		Templates:  templates,
		Start:      start,
		MaxSteps:   len(flow.Steps),
		TokenVar:   flow.Capture.Token,
		SubjectVar: flow.Capture.Subject,
	}, nil
}

// compileSignupSteps builds a scenario graph + templates from a list of signup (or
// teardown) steps: nodes in order, a transition edge between consecutive steps, and
// a dependency edge wherever a step declares DependsOn. It mirrors scenariofile's
// buildGraph/buildTemplates but works on the domain-shaped SignupStep (already
// method/path, no shorthand to parse), so the api layer can compile a flow without
// importing scenariofile.
func compileSignupSteps(steps []domain.SignupStep, kind string) (domain.ScenarioGraph, map[domain.ID]domain.APITemplate, error) {
	if len(steps) == 0 {
		return domain.ScenarioGraph{}, nil, fmt.Errorf("api: %s flow has no steps", kind)
	}
	templates := make(map[domain.ID]domain.APITemplate, len(steps))
	nodes := make([]domain.Node, 0, len(steps))
	seen := make(map[domain.ID]bool, len(steps))
	for _, st := range steps {
		if err := st.Validate(); err != nil {
			return domain.ScenarioGraph{}, nil, fmt.Errorf("api: %s flow: %w", kind, err)
		}
		if seen[st.ID] {
			return domain.ScenarioGraph{}, nil, fmt.Errorf("api: %s flow: duplicate step id %q", kind, st.ID)
		}
		seen[st.ID] = true
		tmplID := domain.ID(string(st.ID) + "__tmpl")
		templates[tmplID] = domain.APITemplate{
			ID:              tmplID,
			Protocol:        domain.ProtocolREST,
			Method:          st.Method,
			Path:            st.Path,
			Headers:         st.Headers,
			PayloadTemplate: st.Body,
			Extract:         st.Extract,
		}
		nodes = append(nodes, domain.Node{ID: st.ID, APITemplateID: tmplID})
	}

	var edges []domain.Edge
	for i := 0; i < len(steps)-1; i++ {
		from, to := steps[i], steps[i+1]
		weight := from.Weight
		if weight == 0 {
			weight = 1
		}
		edges = append(edges, domain.Edge{
			From:       from.ID,
			To:         to.ID,
			Weight:     weight,
			Dependency: to.DependsOn == from.ID,
		})
	}
	// A DependsOn pointing somewhere other than the immediately preceding step
	// becomes an explicit precondition edge (weight 0, Dependency true), matching
	// scenariofile's buildGraph.
	for i, st := range steps {
		if st.DependsOn == "" {
			continue
		}
		if !seen[st.DependsOn] {
			return domain.ScenarioGraph{}, nil, fmt.Errorf("api: %s flow: step %q dependsOn unknown step %q", kind, st.ID, st.DependsOn)
		}
		if i > 0 && steps[i-1].ID == st.DependsOn {
			continue
		}
		edges = append(edges, domain.Edge{From: st.DependsOn, To: st.ID, Weight: 0, Dependency: true})
	}
	graph := domain.ScenarioGraph{ID: domain.ID(kind), Nodes: nodes, Edges: edges}
	if err := graph.Validate(); err != nil {
		return domain.ScenarioGraph{}, nil, fmt.Errorf("api: %s flow: %w", kind, err)
	}
	return graph, templates, nil
}
