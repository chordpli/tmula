package load

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/engine"
)

// SignupFlow is the compiled standalone signup flow a bootstrap-signup pool
// provisions accounts from: a graph, its templates, the entry node, and the
// response captures that become the minted credential. The orchestrator compiles a
// declarative domain.SignupFlow (raw steps + a capture mapping) into this value
// ABOVE the load runner and hands it to NewSignupRunner — the same compile-high /
// run-low direction the main run path and the login transport use. It carries no
// secret: the token is captured at run time from the live signup response.
//
// SignupRunner lives in package load (not api, like the login transport) on
// purpose: load already owns Render/Send/ExtractVariables and the findings-isolated
// RunOnce, and it imports neither auth nor scenariofile, so building the signup
// walk here introduces no import cycle. The orchestrator wires the returned func in
// as auth.ProviderDeps.Signup.
type SignupFlow struct {
	Graph     domain.ScenarioGraph
	Templates map[domain.ID]domain.APITemplate
	Start     domain.ID
	MaxSteps  int
	// TokenVar names the captured variable that becomes the credential's secret
	// (required). SubjectVar, when set, names the captured variable that becomes the
	// non-sensitive subject (the account id, also threaded into teardown).
	TokenVar   string
	SubjectVar string
}

// signupMaxStepsDefault bounds a signup flow's walk when the flow does not set its
// own — generous enough for a multi-hop signup, small enough to stop a runaway.
const signupMaxStepsDefault = 8

// SignupFunc provisions one account and returns its credential. It mirrors
// auth.SignupFunc (load must not import auth), so the orchestrator can pass the
// value NewSignupRunner returns straight into auth.ProviderDeps.Signup.
type SignupFunc func(ctx context.Context, userIndex int) (domain.Credential, error)

// SignupRetry bounds the idempotent retry of a signup walk. A transient failure
// (429 rate-limited or a 5xx) is retried with exponential backoff up to
// MaxAttempts; a deterministic 409 (account already exists) is treated as success,
// not retried. Every retry still flows through the runner's guard, so the burst
// respects the run's rate cap. The Sleep hook is injectable so tests assert the
// backoff schedule on a virtual clock without real waiting.
type SignupRetry struct {
	// MaxAttempts is the total number of signup attempts (>=1). 1 disables retry.
	MaxAttempts int
	// BaseDelay is the first backoff; each subsequent backoff doubles it up to
	// MaxDelay. Zero defaults to a small base.
	BaseDelay time.Duration
	// MaxDelay caps the per-backoff wait so the schedule does not grow unbounded.
	// Zero defaults to a sane ceiling.
	MaxDelay time.Duration
	// Sleep waits for d or returns false if ctx is cancelled first. Defaults to a
	// real cancellable timer; tests inject a virtual clock.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// signupRetryDefaults fills the zero fields of a SignupRetry with safe values: one
// attempt (no retry) is the default when MaxAttempts is unset, so an un-configured
// signup behaves exactly like the single-walk version.
func (s SignupRetry) withDefaults() SignupRetry {
	if s.MaxAttempts < 1 {
		s.MaxAttempts = 1
	}
	if s.BaseDelay <= 0 {
		s.BaseDelay = 200 * time.Millisecond
	}
	if s.MaxDelay <= 0 {
		s.MaxDelay = 30 * time.Second
	}
	if s.Sleep == nil {
		s.Sleep = sleep // the package-level cancellable real timer
	}
	return s
}

// SignupOption customizes a signup runner.
type SignupOption func(*signupConfig)

type signupConfig struct {
	retry SignupRetry
}

// WithSignupRetry makes the signup runner retry transient failures (429/5xx) with
// bounded, cancellable backoff. Without it a signup is a single walk (a 429/5xx
// fails immediately), preserving the original behavior.
func WithSignupRetry(r SignupRetry) SignupOption {
	return func(c *signupConfig) { c.retry = r }
}

// NewSignupRunner compiles a signup flow into a SignupFunc: each call walks the
// flow once (via the findings-isolated RunOnce, so the provisioning traffic never
// lands in the run's observations or findings), captures the token (and subject)
// from the response, and returns a domain.Credential. The runner must already be
// wired with the run's safety guard so the signup endpoint is allowlist-checked and
// rate-capped exactly like the simulated traffic.
//
// userIndex is threaded into the render context as {{.userIndex}} and into the
// per-identity seed (baseSeed + userIndex) so each signup walk is deterministic and
// the flow can template the index it is provisioning for. A signup that succeeds
// but captures no token is an error: the caller must fail rather than authenticate
// as nobody. The walk is a single pass; idempotency/backoff are layered on in a
// later step.
func NewSignupRunner(runner *Runner, flow SignupFlow, baseSeed int64, opts ...SignupOption) (SignupFunc, error) {
	if flow.TokenVar == "" {
		return nil, fmt.Errorf("load: signup flow needs a token capture variable")
	}
	if flow.Start == "" {
		return nil, fmt.Errorf("load: signup flow needs a start node")
	}
	maxSteps := flow.MaxSteps
	if maxSteps <= 0 {
		maxSteps = signupMaxStepsDefault
	}
	cfg := signupConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	retry := cfg.retry.withDefaults()
	// Resolve the flow's node→template map once and reuse it across every signup,
	// mirroring how the main run path and the login transport resolve templates a
	// single time.
	nodeTmpl, err := runner.ResolveNodeTemplates(flow.Graph)
	if err != nil {
		return nil, fmt.Errorf("load: compile signup flow: %w", err)
	}

	return func(ctx context.Context, userIndex int) (domain.Credential, error) {
		delay := retry.BaseDelay
		var lastErr error
		for attempt := 0; attempt < retry.MaxAttempts; attempt++ {
			if ctx.Err() != nil {
				return domain.Credential{}, ctx.Err()
			}
			user := VirtualUser{
				ID:   "signup-" + strconv.Itoa(userIndex),
				Vars: map[string]string{"userIndex": strconv.Itoa(userIndex)},
			}
			vars, retryable, err := runner.signupWalk(ctx, flow, nodeTmpl, maxSteps, user, baseSeed+int64(userIndex))
			if err == nil {
				token := vars[flow.TokenVar]
				if token == "" {
					return domain.Credential{}, fmt.Errorf("load: signup user %d captured no token from variable %q", userIndex, flow.TokenVar)
				}
				cred := domain.Credential{Secret: token}
				if flow.SubjectVar != "" {
					cred.Subject = vars[flow.SubjectVar]
				}
				return cred, nil
			}
			lastErr = err
			if !retryable {
				// A deterministic failure (a 4xx other than 409, a bad request, a
				// non-recoverable extract) is not improved by retrying.
				return domain.Credential{}, fmt.Errorf("load: signup user %d: %w", userIndex, err)
			}
			// Last attempt: do not back off, just return the error below.
			if attempt == retry.MaxAttempts-1 {
				break
			}
			if !retry.Sleep(ctx, delay) {
				return domain.Credential{}, fmt.Errorf("load: signup user %d: backoff cancelled: %w", userIndex, ctx.Err())
			}
			delay *= 2
			if delay > retry.MaxDelay {
				delay = retry.MaxDelay
			}
		}
		return domain.Credential{}, fmt.Errorf("load: signup user %d failed after %d attempts: %w", userIndex, retry.MaxAttempts, lastErr)
	}, nil
}

// signupWalk walks a signup flow once and classifies the outcome for the retry
// loop. It mirrors RunOnce's findings-isolated walk (no result/event sink), but
// understands signup-specific statuses on each request:
//
//   - 2xx → capture the step's Extract and continue (or, on the last step, return
//     the captured variables).
//   - 409 (Conflict) → idempotency: the account already exists, which is SUCCESS.
//     The token is captured from the 409 body when present (a server that echoes the
//     existing credential), else from a later recover/login step in the same flow.
//     The walk continues so a recover step after the conflicting one can run; if the
//     flow ends with no captured token, NewSignupRunner reports the clear "captured
//     no token" error.
//   - 429 / 5xx → transient: returned as retryable so the caller backs off.
//   - any other non-2xx → a deterministic error (not retryable).
//
// retryable is meaningful only when err != nil.
func (r *Runner) signupWalk(ctx context.Context, flow SignupFlow, nodeTmpl map[domain.ID]domain.APITemplate, maxSteps int, u VirtualUser, seed int64) (vars map[string]string, retryable bool, err error) {
	walker, werr := engine.NewWalker(flow.Graph, seed)
	if werr != nil {
		return nil, false, fmt.Errorf("signup build walker: %w", werr)
	}
	path, werr := walker.Walk(flow.Start, maxSteps)
	if werr != nil {
		return nil, false, fmt.Errorf("signup walk: %w", werr)
	}

	vars = copyVars(u.Vars)
	for _, nodeID := range path {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		tmpl, ok := nodeTmpl[nodeID]
		if !ok {
			continue // pure-state node: no request
		}
		req, rerr := Render(tmpl, r.baseURL, u.Cred, vars)
		if rerr != nil {
			return nil, false, fmt.Errorf("signup render node %q: %w", nodeID, rerr)
		}
		resp, serr := r.send(ctx, req)
		if serr != nil {
			// A transport error (or a guard kill) — treat as transient so the burst
			// backs off; a cancelled context surfaces through ctx.Err on the next loop.
			return nil, true, fmt.Errorf("signup send node %q: %w", nodeID, serr)
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			// success: fall through to extraction below
		case resp.StatusCode == http.StatusConflict:
			// 409 = account exists = success. Capture whatever token the conflict
			// response carries; a recover step later in the flow may also supply one.
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			return nil, true, fmt.Errorf("signup node %q returned status %d (transient)", nodeID, resp.StatusCode)
		default:
			return nil, false, fmt.Errorf("signup node %q returned status %d", nodeID, resp.StatusCode)
		}
		if len(tmpl.Extract) > 0 {
			extracted, eerr := ExtractVariables(resp.Body, tmpl.Extract)
			if eerr != nil {
				// A 409 with an unparseable/absent body is not fatal here — a recover
				// step may still capture the token; the final no-token check decides.
				if resp.StatusCode == http.StatusConflict {
					continue
				}
				return nil, false, fmt.Errorf("signup extract node %q: %w", nodeID, eerr)
			}
			for k, v := range extracted {
				vars[k] = v
			}
		}
	}
	return vars, false, nil
}
