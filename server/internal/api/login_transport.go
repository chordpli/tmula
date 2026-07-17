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
		return cred, nil
	}, nil
}
