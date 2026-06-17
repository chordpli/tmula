package load

import (
	"context"
	"fmt"
	"strconv"

	"github.com/chordpli/tmula/server/internal/domain"
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
func NewSignupRunner(runner *Runner, flow SignupFlow, baseSeed int64) (SignupFunc, error) {
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
	// Resolve the flow's node→template map once and reuse it across every signup,
	// mirroring how the main run path and the login transport resolve templates a
	// single time.
	nodeTmpl, err := runner.ResolveNodeTemplates(flow.Graph)
	if err != nil {
		return nil, fmt.Errorf("load: compile signup flow: %w", err)
	}

	return func(ctx context.Context, userIndex int) (domain.Credential, error) {
		user := VirtualUser{
			ID:   "signup-" + strconv.Itoa(userIndex),
			Vars: map[string]string{"userIndex": strconv.Itoa(userIndex)},
		}
		vars, err := runner.RunOnce(ctx, flow.Graph, nodeTmpl, flow.Start, maxSteps, user, baseSeed+int64(userIndex))
		if err != nil {
			return domain.Credential{}, fmt.Errorf("load: signup user %d: %w", userIndex, err)
		}
		token := vars[flow.TokenVar]
		if token == "" {
			return domain.Credential{}, fmt.Errorf("load: signup user %d captured no token from variable %q", userIndex, flow.TokenVar)
		}
		cred := domain.Credential{Secret: token}
		if flow.SubjectVar != "" {
			cred.Subject = vars[flow.SubjectVar]
		}
		return cred, nil
	}, nil
}
