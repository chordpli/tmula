package api

import (
	"context"
	"fmt"
	"strconv"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// LoginFlow is the compiled standalone login flow a CredLogin pool mints tokens
// from. The control plane compiles a scenario's login authoring block (graph +
// templates + the capture mapping) into this domain-shaped value ABOVE the load
// runner, then hands it DOWN to NewLoginTokenFunc — runspec stays a leaf and never
// imports load, and the flow compiler direction (compile high, run low) is the
// same one the main run path uses (buildGraph/buildTemplates → runner).
//
// TokenVar names the captured variable that becomes the credential's secret. It is
// OPTIONAL: when empty (and the flow's extract does not yield it), the transport
// falls back to auto-detecting the token from the final login response (body +
// Set-Cookie) via load.DetectCredential. An explicit TokenVar is authoritative and
// always wins. SubjectVar, when set, names the captured variable that becomes the
// non-sensitive subject (a principal id for evidence); when empty it too is
// auto-detected. The login flow itself is a normal graph: a single POST is the
// common case, but a multi-step login (fetch a CSRF token, then POST it) works
// because RunOnce threads each step's captures into the next.
type LoginFlow struct {
	Graph      domain.ScenarioGraph
	Templates  map[domain.ID]domain.APITemplate
	Start      domain.ID
	MaxSteps   int
	TokenVar   string
	SubjectVar string
}

// loginMaxStepsDefault bounds a login flow's walk when the flow does not set its
// own — generous enough for a multi-hop login, small enough to stop a runaway.
const loginMaxStepsDefault = 8

// NewLoginTokenFunc compiles a login flow into an auth.TokenFunc: each call walks
// the flow once (via the findings-isolated load.RunOnce, so the minted traffic
// never lands in the run's observations), captures the token (and subject) from
// the response, and returns a domain.Credential. The runner must already be wired
// with the run's safety guard so the login endpoint is allowlist-checked and rate-
// capped exactly like the simulated traffic.
//
// userIndex is threaded into the render context as {{.userIndex}} and into the
// per-principal seed (baseSeed + userIndex) so each login is deterministic and a
// flow can template the index it is minting for. A login that succeeds but yields
// no token — neither an explicit capture nor an auto-detected one — is an error:
// the caller must fail rather than authenticate as nobody.
//
// TokenVar is optional: an empty TokenVar means "auto-detect", so a login flow can
// omit the extract+capture boilerplate for the common token shapes (see
// load.DetectCredential). When TokenVar is set, the captured variable is
// authoritative and auto-detection is not consulted for the token.
func NewLoginTokenFunc(runner *load.Runner, flow LoginFlow, baseSeed int64) (auth.TokenFunc, error) {
	if flow.Start == "" {
		return nil, fmt.Errorf("api: login flow needs a start node")
	}
	maxSteps := flow.MaxSteps
	if maxSteps <= 0 {
		maxSteps = loginMaxStepsDefault
	}
	// Resolve the flow's node→template map once and reuse it across every mint and
	// re-mint, mirroring how the main run path resolves templates a single time.
	nodeTmpl, err := runner.ResolveNodeTemplates(flow.Graph)
	if err != nil {
		return nil, fmt.Errorf("api: compile login flow: %w", err)
	}

	return func(ctx context.Context, userIndex int) (domain.Credential, error) {
		user := load.VirtualUser{
			ID:   "login-" + strconv.Itoa(userIndex),
			Vars: map[string]string{"userIndex": strconv.Itoa(userIndex)},
		}
		vars, final, err := runner.RunOnceCapture(ctx, flow.Graph, nodeTmpl, flow.Start, maxSteps, user, baseSeed+int64(userIndex))
		if err != nil {
			return domain.Credential{}, fmt.Errorf("api: login user %d: %w", userIndex, err)
		}
		// Explicit capture is authoritative; auto-detect only fills what was not
		// explicitly captured. Detect once so the token and (when not explicitly
		// named) the subject come from the same response.
		var autoToken, autoSubject string
		if flow.TokenVar == "" || flow.SubjectVar == "" {
			autoToken, autoSubject = load.DetectCredential(final.Body, final.SetCookie)
		}

		token := vars[flow.TokenVar]
		if flow.TokenVar == "" {
			token = autoToken
		}
		if token == "" {
			if flow.TokenVar == "" {
				return domain.Credential{}, fmt.Errorf("api: login user %d: could not auto-detect a token in the login response; set an explicit capture path", userIndex)
			}
			return domain.Credential{}, fmt.Errorf("api: login user %d captured no token from variable %q", userIndex, flow.TokenVar)
		}
		cred := domain.Credential{Secret: token}
		switch {
		case flow.SubjectVar != "":
			cred.Subject = vars[flow.SubjectVar]
		default:
			cred.Subject = autoSubject
		}
		return cred, nil
	}, nil
}
