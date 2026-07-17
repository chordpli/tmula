package api

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

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

// signupRetryAttempts and signupRetryBaseDelay tune the idempotent provisioning
// retry: a transient 429/5xx is retried a few times with exponential backoff
// starting at the base delay, so a flaky or rate-limited signup endpoint does not
// fail the run on the first hiccup. A deterministic 409 is success and is never
// retried.
const (
	signupRetryAttempts  = 4
	signupRetryBaseDelay = 200 * time.Millisecond
)

// bootstrapAuth bundles the runtime pieces a bootstrap-signup run is driven by: the
// signup provider (cache-by-index + in-flight dedup + Prewarm) and the
// effective prewarm concurrency. It is built once per run, above the load runner,
// from the spec's compiled signup flow. A later PR adds the teardown half.
type bootstrapAuth struct {
	provider *auth.BootstrapSignupProvider
	// prewarmConcurrency bounds the provisioning burst: min(RateCap.MaxConcurrency,
	// bootstrapMaxConcurrency), always at least 1.
	prewarmConcurrency int
	// keepAccounts records the run's opt-out: when true the provider was built with
	// no teardown func, so its Teardown is a cache-clearing no-op and the accounts
	// are intentionally left in place (for a later reproduce under the same live
	// principals).
	keepAccounts bool
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
	sf := *spec.CredentialPool.SignupFlow
	flow, err := compileSignupFlow(sf)
	if err != nil {
		return nil, fmt.Errorf("api: compile signup flow: %w", err)
	}
	// A dedicated runner for the signup flow: same adapter and base URL as the run,
	// guarded so the signup endpoint is allowlist-checked and rate-capped. It carries
	// no result/event sink, so RunOnce (which the transport drives) stays findings-
	// isolated even if those were set.
	runner := load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, flow.Templates, load.WithGuard(guard))
	// Idempotent provisioning: retry a transient 429/5xx with bounded, cancellable
	// backoff (a deterministic 409 = success is handled inside the walk). Every retry
	// still flows through the guarded runner, so the burst respects the run's rate
	// cap. The default clock is a real cancellable timer.
	signup, err := load.NewSignupRunner(runner, flow, spec.Seed, load.WithSignupRetry(load.SignupRetry{
		MaxAttempts: signupRetryAttempts,
		BaseDelay:   signupRetryBaseDelay,
	}))
	if err != nil {
		return nil, fmt.Errorf("api: compile signup flow: %w", err)
	}
	provider, err := auth.NewBootstrapSignupProvider(auth.SignupFunc(signup))
	if err != nil {
		return nil, fmt.Errorf("api: build bootstrap provider: %w", err)
	}
	// Wire the teardown transport when the flow declares a teardown journey and the
	// run did not opt out with keep-accounts. A nil teardown makes the provider's
	// Teardown a cache-clearing no-op (the keep-accounts path), which is exactly the
	// gating-safety contract: a runnable bootstrap pool either has a teardown or an
	// explicit keep-accounts opt-out (enforced by Validate on the run path).
	if sf.HasTeardown() && !spec.CredentialPool.KeepAccounts {
		teardown, err := s.teardownFuncFor(spec, sf, guard)
		if err != nil {
			return nil, fmt.Errorf("api: compile teardown flow: %w", err)
		}
		provider.SetTeardown(teardown)
	}
	return &bootstrapAuth{
		provider:           provider,
		prewarmConcurrency: effectivePrewarmConcurrency(spec.TargetEnv.RateCap.MaxConcurrency),
		keepAccounts:       spec.CredentialPool.KeepAccounts,
	}, nil
}

// teardownFuncFor compiles a signup flow's teardown journey into an auth.TeardownFunc:
// each call walks the teardown flow once through the findings-isolated RunOnce,
// rendering the provisioned account's subject as {{.subject}} (and its index as
// {{.userIndex}}), so a "DELETE /accounts/{{.subject}}" removes the exact account
// that was provisioned. The teardown runner is guarded like the run and the signup,
// and carries no result/event sink, so deprovision traffic produces zero findings.
func (s *Server) teardownFuncFor(spec RunSpec, sf domain.SignupFlow, guard *safety.Guard) (auth.TeardownFunc, error) {
	graph, templates, err := compileSignupSteps(sf.Teardown, "teardown")
	if err != nil {
		return nil, err
	}
	start := sf.TeardownStart
	if start == "" {
		start = sf.Teardown[0].ID
	}
	runner := load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, templates, load.WithGuard(guard))
	nodeTmpl, err := runner.ResolveNodeTemplates(graph)
	if err != nil {
		return nil, fmt.Errorf("api: compile teardown flow: %w", err)
	}
	maxSteps := len(sf.Teardown)
	return func(ctx context.Context, userIndex int, cred domain.Credential) error {
		user := load.VirtualUser{
			ID:   "teardown-" + strconv.Itoa(userIndex),
			Cred: cred,
			Vars: map[string]string{
				"userIndex": strconv.Itoa(userIndex),
				"subject":   cred.Subject,
			},
		}
		// The teardown walk is findings-isolated (RunOnce touches no sink) and seeded
		// off the run seed + index, mirroring the signup walk for the same identity.
		_, err := runner.RunOnce(ctx, graph, nodeTmpl, start, maxSteps, user, spec.Seed+int64(userIndex))
		return err
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

// teardownBaseTimeout and teardownPerAccount scale the deprovision budget with the
// pool size: a fresh context with a timeout of base + perAccount*n, capped, so a
// large pool gets enough time to tear down without an unbounded hang on a wedged
// teardown endpoint.
const (
	teardownBaseTimeout = 30 * time.Second
	teardownPerAccount  = 50 * time.Millisecond
	teardownMaxTimeout  = 10 * time.Minute
)

// runTeardown deprovisions the run's bootstrap accounts. It is invoked from a
// deferred call on the run-execution goroutine AFTER the run finishes (or is
// killed), and it runs Teardown on a FRESH context.Background() — never the run
// context — with a timeout scaled to the pool size, so a killed or timed-out run
// still deprovisions every account it created. A keep-accounts run skips it
// entirely (the provider has no teardown func anyway, but skip avoids the no-op
// churn). Teardown is best-effort: an orphaned account is logged at ERROR inside
// the provider and the aggregated error is logged here, never propagated into the
// run's status — the load result stands on its own.
func (b *bootstrapAuth) runTeardown(runID domain.ID, poolSize int) {
	if b == nil || b.keepAccounts {
		return
	}
	timeout := teardownBaseTimeout + time.Duration(poolSize)*teardownPerAccount
	if timeout > teardownMaxTimeout {
		timeout = teardownMaxTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := b.provider.Teardown(ctx); err != nil {
		// Best-effort: orphans are already logged per-account at ERROR by the
		// provider; surface the aggregate here too, but never fail the run for it.
		slog.Error("bootstrap teardown incomplete", "run", runID, "err", err)
	}
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
