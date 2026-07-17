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
	// Entries, when present, is the credential POOL of login-INPUT rows the P8
	// multi-user login path logs in with: each domain.Credential is a row where
	// Subject is the username and Secret is the password (NOT a pre-issued token).
	// Virtual user i logs in with row i (wrapping: entries[i % len(entries)]) — the
	// transport seeds the row into the render context as {{.username}}/{{.password}}
	// (plus {{.subject}}/{{.secret}} aliases) BEFORE walking the flow, so a login
	// body like {"username":"{{.username}}","password":"{{.password}}"} authenticates
	// each VU as a different account, each minting (and re-minting on expiry) its own
	// token. The minted credential's Subject defaults to the row's username so a
	// finding/reproduce identifies which user — unless an explicit SubjectVar (or an
	// auto-detected subject) overrides it. Empty Entries is the unchanged
	// single-identity login: no row is seeded and {{.username}}/{{.password}} render
	// empty. The passwords are in-process secrets (Credential.Secret is json:"-").
	Entries []domain.Credential

	// RefreshRequest / RefreshBody are an OPTIONAL explicit refresh-grant override. When
	// RefreshBody is set, refreshTemplateFor builds the mid-run refresh transport from it
	// — SHORT-CIRCUITING the deriveRefreshTemplate gate — so even a login the auto-derive
	// cannot rewrite (a JSON-body login, or a form login with no grant_type) still gets a
	// real grant_type=refresh_token exchange instead of a re-login. RefreshRequest is the
	// "METHOD /path" the refresh POSTs to; it is OPTIONAL and defaults to the login token
	// node's endpoint when empty (a same-endpoint refresh needs only the body). RefreshBody
	// is the form body the operator authored; a bare {{.refreshToken}} in it is routed
	// through urlquery (the SAME convention as the auto-derived body) so an opaque token
	// stays form-safe. Both empty is the unchanged auto-derive / re-login behavior. They
	// carry no secret — the refresh token is captured from the live login response.
	RefreshRequest string
	RefreshBody    string
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
		// Seed the render context for this mint. userIndex is always threaded so a flow
		// can template the index it is minting for. The username/password/secret keys
		// are ALWAYS seeded (the render uses missingkey=error, so a flow that references
		// {{.username}} must find the key) — they carry the login-INPUT row for the P8
		// multi-user path and render EMPTY on the single-identity path (no entries).
		//
		// The row is ALSO seeded onto the login user's Cred so Render's built-in
		// {{.subject}} (and {{.token}}) reflect the row's username/password: Render
		// unconditionally sets ctx["subject"]=cred.Subject / ctx["token"]=cred.Secret
		// AFTER copying Vars, so a Vars-only "subject" would be clobbered to the (empty)
		// login credential. Putting the row on Cred makes {{.subject}} the row username.
		// {{.secret}} is a Vars-only alias (Render exposes the secret as "token", not
		// "secret"), so it is seeded explicitly. With no entries the row is the zero
		// credential, so every key renders empty — any flow that did not reference these
		// keys is byte-for-byte the previous single-identity login.
		var row domain.Credential
		hasRow := len(flow.Entries) > 0
		if hasRow {
			row = flow.Entries[userIndex%len(flow.Entries)]
		}
		vars := map[string]string{
			"userIndex": strconv.Itoa(userIndex),
			"username":  row.Subject,
			"password":  row.Secret,
			"secret":    row.Secret,
		}
		user := load.VirtualUser{
			ID:   "login-" + strconv.Itoa(userIndex),
			Vars: vars,
			// Render exposes Cred.Subject as {{.subject}} and Cred.Secret as {{.token}}.
			Cred: domain.Credential{Subject: row.Subject, Secret: row.Secret},
		}
		captured, final, err := runner.RunOnceCapture(ctx, flow.Graph, nodeTmpl, flow.Start, maxSteps, user, baseSeed+int64(userIndex))
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

		token := captured[flow.TokenVar]
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
		// Subject precedence: an explicit SubjectVar wins, then an auto-detected
		// subject, then the login-input row's username (so a multi-user finding/
		// reproduce identifies which account). The single-identity path (no row) keeps
		// the SubjectVar-or-auto-detect behavior exactly as before.
		switch {
		case flow.SubjectVar != "":
			cred.Subject = captured[flow.SubjectVar]
		case autoSubject != "":
			cred.Subject = autoSubject
		case hasRow:
			cred.Subject = row.Subject
		}
		// Fold in the OAuth2 refresh grant data when the response carries it — the
		// data foundation a later real grant_type=refresh_token transport reads. This
		// is purely additive: a response with no refresh_token/expires_in leaves both
		// fields zero, so the returned credential is identical to a pre-refresh mint.
		cred.Refresh, cred.ExpiresIn = load.DetectRefresh(final.Body)
		return cred, nil
	}, nil
}

// refreshNodeID and refreshGraphID label the synthetic single-node graph the refresh
// transport walks. The refresh exchange is a single token POST, so a one-node flow
// (the derived refresh template) is sufficient; the labels are internal and never
// observed by the run.
const (
	refreshNodeID  = domain.ID("refresh")
	refreshGraphID = domain.ID("refresh-flow")
)

// NewRefreshTokenFunc compiles a derived refresh template into an
// auth.RefreshTokenFunc: each call renders the template with the CURRENT credential
// seeded (so {{.refreshToken}} carries cur.Refresh), POSTs it through the SAME
// guarded runner the login uses (so the safety allowlist and rate cap still apply),
// and captures the rotated credential from the response. It is the real OAuth2
// grant_type=refresh_token exchange that replaces re-running the (human-consent)
// login on a mid-run 401.
//
// The render context seeds refreshToken (cur.Refresh) and token (cur.Secret) via the
// VirtualUser's Vars, and subject (cur.Subject) via the user's Cred, exactly like
// NewLoginTokenFunc seeds username/password — Render uses missingkey=error, so the
// keys are ALWAYS seeded. baseSeed+userIndex keeps the walk deterministic per
// principal, mirroring the login transport.
//
// Capture precedence mirrors the login: the new access token is auto-detected via
// load.DetectCredential (body + Set-Cookie), and the refresh token / lifetime via
// load.DetectRefresh. ROTATION RULE: when the response omits a new refresh_token, the
// current credential's refresh token is CARRIED FORWARD (not blanked), so a server
// that does not rotate the refresh token keeps a working one for the next cycle.
// cur.Subject is preserved. A refresh that yields no access token is a loud error
// (mirroring NewLoginTokenFunc), rather than authenticating as nobody.
func NewRefreshTokenFunc(runner *load.Runner, refreshTmpl domain.APITemplate, baseSeed int64) auth.RefreshTokenFunc {
	graph := domain.ScenarioGraph{
		ID:    refreshGraphID,
		Nodes: []domain.Node{{ID: refreshNodeID, APITemplateID: refreshTmpl.ID}},
	}
	nodeTmpl := map[domain.ID]domain.APITemplate{refreshNodeID: refreshTmpl}

	return func(ctx context.Context, userIndex int, cur domain.Credential) (domain.Credential, error) {
		// Seed the render context from the CURRENT credential. refreshToken/token are
		// Vars (Render exposes only subject/token from Cred, so refreshToken must be a
		// Var); subject/token also come from Cred so {{.subject}}/{{.token}} reflect the
		// current principal. All keys are always seeded (missingkey=error).
		vars := map[string]string{
			"userIndex":    strconv.Itoa(userIndex),
			"refreshToken": cur.Refresh,
			"token":        cur.Secret,
		}
		user := load.VirtualUser{
			ID:   "refresh-" + strconv.Itoa(userIndex),
			Vars: vars,
			Cred: domain.Credential{Subject: cur.Subject, Secret: cur.Secret},
		}
		_, final, err := runner.RunOnceCapture(ctx, graph, nodeTmpl, refreshNodeID, 1, user, baseSeed+int64(userIndex))
		if err != nil {
			return domain.Credential{}, fmt.Errorf("api: refresh-token exchange user %d: %w", userIndex, err)
		}
		// Capture the rotated access token (and any subject) from the response, then the
		// rotated refresh token / lifetime — mirroring the login's auto-detect.
		token, _ := load.DetectCredential(final.Body, final.SetCookie)
		if token == "" {
			return domain.Credential{}, fmt.Errorf("api: refresh-token exchange user %d: response carried no access token", userIndex)
		}
		refresh, expiresIn := load.DetectRefresh(final.Body)
		if refresh == "" {
			// Rotation rule: the server did not rotate the refresh token, so keep the
			// current one rather than blanking it (a blanked refresh token would force a
			// re-login on the next cycle).
			refresh = cur.Refresh
		}
		return domain.Credential{
			Subject:   cur.Subject, // preserved across the rotation
			Secret:    token,
			Refresh:   refresh,
			ExpiresIn: expiresIn,
		}, nil
	}
}
