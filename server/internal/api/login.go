package api

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/engine"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/safety"
)

// loginAuth bundles the runtime pieces a CredLogin run is driven by: the login
// provider (cache-by-index + dedup + refresh) and the per-index seed used to make
// each login deterministic. It is built once per run, above the load runner, and
// drives both the initial token mint (Prewarm/Acquire) and the mid-run refresh.
type loginAuth struct {
	provider *auth.LoginProvider
	// shared is true for the client_credentials scope: every user shares one token
	// (cache key 0) and one holder. Per-user mints one token per index.
	shared bool
	// prewarmConcurrency bounds the per-user prewarm burst: min(RateCap.MaxConcurrency,
	// loginMaxPrewarmConcurrency), so priming 200k tokens is parallel yet never wider
	// than the run's rate cap — the prewarm must not itself load-test the IdP.
	prewarmConcurrency int

	// sharedMu guards the lazy construction of the single shared holder. In the
	// shared scope every seed() returns the SAME holder POINTER (and the same
	// refresh closure bound to it), built exactly once here, so one mint serves all
	// sessions and one refresh rotates the token for all of them. It is a pointer,
	// never copied — copying it would give each session an independent token box and
	// silently break the shared (client_credentials) semantics.
	sharedMu      sync.Mutex
	sharedHolder  load.CredentialHolder
	sharedRefresh load.RefreshFunc
}

// loginAuthFor builds the login provider for a CredLogin run by compiling the
// spec's login flow into a token transport and wrapping it in a LoginProvider. It
// returns (nil, nil) for any non-login pool, so callers can branch on it. The
// login runner is guarded by the run's safety policy so the login endpoint obeys
// the same allowlist and rate cap as the simulated traffic. liveRefresh selects
// whether the provider mints live (the run path) — it is always true here; the
// refresh-FREE reproduce variant builds its own provider without wiring a refresh
// onto the user (see reproduce.go).
func (s *Server) loginAuthFor(spec RunSpec, guard *safety.Guard) (*loginAuth, error) {
	if spec.CredentialPool == nil || spec.CredentialPool.Strategy != domain.CredLogin {
		return nil, nil
	}
	flow, runner, err := s.compileLoginFlow(spec, guard)
	if err != nil {
		return nil, err
	}
	tokenFunc, err := NewLoginTokenFunc(runner, flow, spec.Seed)
	if err != nil {
		return nil, fmt.Errorf("api: compile login flow: %w", err)
	}
	// Build the mid-run refresh transport from the login flow. An explicit override on
	// the flow wins (refreshTemplateFor checks it first); else a real grant_type=
	// refresh_token transport is auto-derived when the token POST is an OAuth2 form
	// grant. When neither yields a template (a non-form login with no override),
	// refreshFunc stays nil and Refresh falls back to re-running the login — the safe
	// default. The refresh exchange runs through the SAME guarded runner as the login,
	// so it obeys the same allowlist and rate cap.
	var refreshFunc auth.RefreshTokenFunc
	if refreshTmpl, ok := refreshTemplateFor(flow); ok {
		refreshRunner := load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, map[domain.ID]domain.APITemplate{refreshTmpl.ID: refreshTmpl}, load.WithGuard(guard))
		refreshFunc = NewRefreshTokenFunc(refreshRunner, refreshTmpl, spec.Seed)
	}
	return newLoginAuthFromToken(tokenFunc, refreshFunc, spec.CredentialPool.EffectiveLoginScope() == domain.LoginShared, spec.TargetEnv.RateCap.MaxConcurrency)
}

// compileLoginFlow compiles a CredLogin spec's login authoring block into the runnable
// LoginFlow plus a guarded runner, the shared front half of loginAuthFor. It is factored
// out so the preflight endpoint can run the login flow ONCE (and detect the token source)
// through the exact same compiled flow and safety guard the run path uses — a preflight
// must never escape the target allowlist. It returns an error for a login pool that
// carries no login flow (a wiring bug Validate normally catches).
func (s *Server) compileLoginFlow(spec RunSpec, guard *safety.Guard) (LoginFlow, *load.Runner, error) {
	if spec.LoginFlow == nil {
		// Validate already rejects this, but guard against a programming error
		// reaching the runtime with no flow to mint from.
		return LoginFlow{}, nil, fmt.Errorf("api: login run has no login flow to mint tokens from")
	}
	flow := LoginFlow{
		Graph: spec.LoginFlow.Graph,
		// Form-urlencoded login bodies get their bare credential-row placeholders
		// ({{.username}}/{{.password}} and aliases) piped through urlquery, so a
		// password carrying &, =, + or a space survives the form decode byte-exact.
		// Same convention as the refresh body (urlqueryRefreshToken); JSON bodies
		// are untouched (raw substitution is correct there).
		Templates:  urlqueryFormLoginTemplates(spec.LoginFlow.Templates),
		Start:      spec.LoginFlow.Start,
		MaxSteps:   spec.LoginFlow.MaxSteps,
		TokenVar:   spec.LoginFlow.TokenVar,
		SubjectVar: spec.LoginFlow.SubjectVar,
		// P8 multi-user login: the pool's Entries are login-INPUT rows (username +
		// password), threaded into the token func so virtual user i logs in as row
		// i%N. They reach BOTH the run path and the reproduce path through this single
		// builder, so reproduce of VU i re-logs-in as the same account deterministically
		// (i%N is a pure function of the index). Empty entries is the single-identity
		// login — unchanged. The CLI resolves a login Source into these Entries at
		// expand time (like the pool strategy), so a login pool only ever arrives here
		// with inline entries, never an unresolved Source.
		Entries: spec.CredentialPool.Entries,
		// The OPTIONAL explicit refresh override (empty ⇒ auto-derive / re-login). It is
		// threaded down so refreshTemplateFor can short-circuit the auto-derive gate.
		RefreshRequest: spec.LoginFlow.RefreshRequest,
		RefreshBody:    spec.LoginFlow.RefreshBody,
	}
	// A dedicated runner for the login flow: same adapter and base URL as the run,
	// guarded so the login endpoint is allowlist-checked and rate-capped. It carries
	// no result/event sink, so RunOnce (which the transport drives) stays findings-
	// isolated even if those were set.
	runner := load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, flow.Templates, load.WithGuard(guard))
	return flow, runner, nil
}

// loginMaxPrewarmConcurrency caps the per-user login prewarm burst regardless of
// how generous the run's rate cap is, so priming a huge account pool does not slam
// the IdP with thousands of simultaneous logins. The effective prewarm concurrency
// is min(this, RateCap.MaxConcurrency).
const loginMaxPrewarmConcurrency = 16

// newLoginAuthFromToken builds a loginAuth over a token func, an OPTIONAL
// refresh-token func, scope, and the run's rate-cap concurrency (which bounds the
// prewarm burst). It is the single construction point for the provider, so the run
// path and tests build the same seam. A nil refresh func keeps the provider's
// Refresh on the re-login fallback path.
func newLoginAuthFromToken(token auth.TokenFunc, refresh auth.RefreshTokenFunc, shared bool, rateCapMaxConcurrency int) (*loginAuth, error) {
	provider, err := auth.NewLoginProvider(token)
	if err != nil {
		return nil, fmt.Errorf("api: build login provider: %w", err)
	}
	provider.SetRefreshToken(refresh) // nil-safe
	return &loginAuth{
		provider:           provider,
		shared:             shared,
		prewarmConcurrency: prewarmConcurrencyFor(rateCapMaxConcurrency, loginMaxPrewarmConcurrency),
	}, nil
}

// cacheKey maps a user/arrival index onto the login provider's cache key. Per-user
// keys by the index so each principal mints its own token; shared collapses every
// index onto key 0 so one token (one client_credentials grant) is minted and
// served to all sessions.
func (l *loginAuth) cacheKey(userIndex int) int {
	if l.shared {
		return 0
	}
	return userIndex
}

// seed mints (or serves the cached) credential for userIndex and returns a live
// holder wired with a refresh closure. The holder is the seam runSession reads the
// credential from per step; the refresh re-runs the login for the SAME cache key
// (the same Seed-offset arithmetic the run path keys credentials by, never a new
// index) and rotates the holder in place on a mid-run 401.
//
// For the shared scope every call returns the SAME holder pointer (built once and
// memoized), so a single refresh reaches every session — see sharedHolder. The
// holder is created here, above the runner, exactly once per principal.
func (l *loginAuth) seed(ctx context.Context, userIndex int) (load.CredentialHolder, load.RefreshFunc, error) {
	if l.shared {
		return l.sharedSeed(ctx)
	}
	key := l.cacheKey(userIndex)
	cred, err := l.provider.Acquire(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	holder := load.NewCredentialHolder(cred)
	refresh := l.refreshFunc(key, holder)
	return holder, refresh, nil
}

// sharedSeed returns the single shared holder (and its refresh), building it once
// on first call. Every session that seeds in the shared scope receives the SAME
// holder pointer, so one client_credentials token is minted and one refresh
// rotates it for all of them.
func (l *loginAuth) sharedSeed(ctx context.Context) (load.CredentialHolder, load.RefreshFunc, error) {
	l.sharedMu.Lock()
	defer l.sharedMu.Unlock()
	if l.sharedHolder != nil {
		return l.sharedHolder, l.sharedRefresh, nil
	}
	cred, err := l.provider.Acquire(ctx, 0) // shared cache key is always 0
	if err != nil {
		return nil, nil, err
	}
	holder := load.NewCredentialHolder(cred)
	l.sharedHolder = holder
	l.sharedRefresh = l.refreshFunc(0, holder)
	return l.sharedHolder, l.sharedRefresh, nil
}

// refreshFunc builds the per-principal refresh closure: it re-acquires the token
// for key and rotates holder. It binds key once, so the runtime never re-derives
// an index — the orchestrator owns the index arithmetic.
func (l *loginAuth) refreshFunc(key int, holder load.CredentialHolder) load.RefreshFunc {
	return func(ctx context.Context) error {
		cred, err := l.provider.Refresh(ctx, key)
		if err != nil {
			return err
		}
		holder.Set(cred)
		return nil
	}
}

// Prewarm mints n tokens ahead of the run (per-user) or the single shared token
// (shared), matching how the run will key them — so the first request of every
// session has a token without a synchronous login on the hot path. The per-user
// burst is bounded (prewarmBounded) so priming a large pool is parallel yet never
// wider than the rate cap; the deduping provider still logs in each index exactly
// once even under concurrency. Shared mints its single token directly.
func (l *loginAuth) Prewarm(ctx context.Context, n int) error {
	if l.shared {
		_, err := l.provider.Acquire(ctx, 0)
		return err
	}
	return prewarmBounded(ctx, n, l.prewarmConcurrency, func(ctx context.Context, idx int) error {
		_, err := l.provider.Acquire(ctx, idx)
		return err
	})
}

// refreshTemplateFor resolves the mid-run refresh transport's request template for a
// login flow, in precedence order:
//
//  1. An EXPLICIT override (flow.RefreshBody set) wins and SHORT-CIRCUITS the auto-
//     derive gate, so even a login deriveRefreshTemplate refuses (a JSON-body login,
//     or a form login with no grant_type) gets a real grant_type=refresh_token
//     exchange from the operator's authored body (see buildOverrideRefreshTemplate).
//  2. Else auto-derivation from an OAuth2 form grant (deriveRefreshTemplate).
//  3. Else (_, false): no refresh transport — Refresh falls back to re-running the
//     login, the safe long-standing default.
//
// It is the single construction point loginAuthFor reads, so the override and the
// auto-derived template feed the SAME template→RefreshTokenFunc wiring below it.
func refreshTemplateFor(flow LoginFlow) (domain.APITemplate, bool) {
	if strings.TrimSpace(flow.RefreshBody) != "" || strings.TrimSpace(flow.RefreshRequest) != "" {
		return buildOverrideRefreshTemplate(flow)
	}
	return deriveRefreshTemplate(flow)
}

// buildOverrideRefreshTemplate builds the refresh template from an EXPLICIT override:
// the method/path come from flow.RefreshRequest ("METHOD /path") when set, else from
// the login token node's endpoint (a same-endpoint refresh needs only a body); the
// body is flow.RefreshBody. It SHORT-CIRCUITS the auto-derive gate — a JSON-body or
// no-grant_type login still gets a refresh transport when the operator authors one.
//
// The override is, by convention, an OAuth2 form (x-www-form-urlencoded) refresh
// grant, so the template is stamped with a form Content-Type regardless of the login
// node's headers (a JSON login's headers would otherwise mis-encode the form body).
// A bare {{.refreshToken}} in the body is routed through urlquery — the SAME convention
// as the auto-derived body — so an opaque/base64 token stays form-safe at render time.
// It returns (_, false) only when there is no body to send (a request-only override is
// not enough to POST a refresh grant).
func buildOverrideRefreshTemplate(flow LoginFlow) (domain.APITemplate, bool) {
	body := strings.TrimSpace(flow.RefreshBody)
	if body == "" {
		return domain.APITemplate{}, false
	}

	// Resolve method/path: an explicit RefreshRequest wins; else default to the login
	// token node's endpoint so a same-endpoint refresh needs only the body.
	method, path := "", ""
	if req := strings.TrimSpace(flow.RefreshRequest); req != "" {
		fields := strings.Fields(req)
		if len(fields) != 2 {
			return domain.APITemplate{}, false // malformed — Validate rejects this earlier
		}
		method, path = strings.ToUpper(fields[0]), fields[1]
	} else {
		tokenTmpl, ok := loginTokenTemplate(flow)
		if !ok {
			return domain.APITemplate{}, false
		}
		method, path = tokenTmpl.Method, tokenTmpl.Path
	}

	return domain.APITemplate{
		ID:       refreshNodeID,
		Protocol: domain.ProtocolREST,
		Method:   method,
		Path:     path,
		// An OAuth2 refresh grant is a form body; stamp the form Content-Type so a JSON
		// login's headers do not mis-encode it.
		Headers:         map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		PayloadTemplate: urlqueryRefreshToken(body),
		Extract:         nil, // the refresh transport auto-detects; no explicit extract
	}, true
}

// refreshTokenPlaceholderRE matches a bare {{.refreshToken}} placeholder — tolerating
// internal whitespace ({{ .refreshToken }}) — that is NOT already piped through a
// builtin. urlqueryRefreshToken rewrites those to {{.refreshToken | urlquery}} so an
// override body the operator authored gets the same form-safe encoding the auto-derived
// body uses; an already-piped {{.refreshToken | urlquery}} is left untouched.
var refreshTokenPlaceholderRE = regexp.MustCompile(`\{\{\s*\.refreshToken\s*\}\}`)

// urlqueryRefreshToken routes a bare {{.refreshToken}} in an override body through
// text/template's urlquery builtin, matching how the auto-derived body encodes the
// captured refresh token (url.QueryEscape at render time). It keeps an opaque/
// standard-base64 token containing +, /, = or a space form-safe so the token endpoint's
// url.ParseQuery decode recovers the exact original token. A body that already pipes
// the placeholder (or does not reference it) is returned unchanged.
func urlqueryRefreshToken(body string) string {
	return refreshTokenPlaceholderRE.ReplaceAllString(body, "{{.refreshToken | urlquery}}")
}

// loginRowPlaceholderRE matches a bare credential-row placeholder — {{.username}},
// {{.password}}, or the {{.subject}}/{{.secret}} aliases, tolerating internal
// whitespace — that is NOT already piped through a builtin. The capture group is
// the variable name, re-emitted with the urlquery pipe.
var loginRowPlaceholderRE = regexp.MustCompile(`\{\{\s*\.(username|password|subject|secret)\s*\}\}`)

// urlqueryFormLoginTemplates returns a copy of a login flow's templates with every
// form-urlencoded body's bare credential-row placeholders piped through urlquery —
// the same convention urlqueryRefreshToken applies to the refresh body — so a
// password carrying &, =, + or a space survives the form decode byte-exact.
// Non-form (JSON) templates are left byte-identical: raw substitution is correct
// there. The input map is never mutated (the spec's templates are shared).
func urlqueryFormLoginTemplates(templates map[domain.ID]domain.APITemplate) map[domain.ID]domain.APITemplate {
	out := make(map[domain.ID]domain.APITemplate, len(templates))
	for id, tmpl := range templates {
		if isFormURLEncoded(tmpl.Headers) && tmpl.PayloadTemplate != "" {
			tmpl.PayloadTemplate = loginRowPlaceholderRE.ReplaceAllString(tmpl.PayloadTemplate, "{{.$1 | urlquery}}")
		}
		out[id] = tmpl
	}
	return out
}

// deriveRefreshTemplate derives a grant_type=refresh_token request template from a
// login flow's token-POST template, so a mid-run refresh can exchange the captured
// refresh token instead of re-running the (often human-consent) login. It returns
// (_, false) — no refresh transport, Refresh re-logins — unless the token node's body
// is application/x-www-form-urlencoded AND carries a grant_type field, the shape an
// OAuth2 refresh grant is expressed over.
//
// The derived template reuses the token node's URL/method/headers, and rewrites the
// form body: DROP grant_type/username/password/code (the password/authorization-code
// grant inputs), KEEP client_id/client_secret/scope/audience and any other fields,
// and PREPEND grant_type=refresh_token & refresh_token={{.refreshToken | urlquery}}
// (the refresh transport seeds {{.refreshToken}} from the current credential; urlquery
// keeps an opaque/base64 token form-safe).
//
// An explicit auth.login.refresh override (flow.RefreshBody) is threaded in AHEAD of
// this auto-derivation by refreshTemplateFor — the override short-circuits the gate, so
// a JSON-body or no-grant_type login can still get a refresh transport. This function
// is the auto path: it stays the behavior when no override is authored.
func deriveRefreshTemplate(flow LoginFlow) (domain.APITemplate, bool) {
	tokenTmpl, ok := loginTokenTemplate(flow)
	if !ok {
		return domain.APITemplate{}, false
	}
	// GATE: only an x-www-form-urlencoded body carrying grant_type is an OAuth2 grant
	// we can rewrite into a refresh grant.
	if !isFormURLEncoded(tokenTmpl.Headers) {
		return domain.APITemplate{}, false
	}
	form, err := url.ParseQuery(tokenTmpl.PayloadTemplate)
	if err != nil {
		return domain.APITemplate{}, false
	}
	if form.Get("grant_type") == "" {
		return domain.APITemplate{}, false
	}

	body := rewriteToRefreshGrant(form)
	refreshTmpl := tokenTmpl
	refreshTmpl.ID = refreshNodeID
	refreshTmpl.Extract = nil // the refresh transport auto-detects; no explicit extract
	refreshTmpl.PayloadTemplate = body
	return refreshTmpl, true
}

// loginTokenTemplate returns the login flow's token-POST template: for the common
// single-request OAuth2 login it is the one request template; for a multi-step login
// it is the LAST request-bearing node on the deterministic walk from flow.Start (the
// node that actually exchanges credentials for a token). It returns (_, false) when
// the walk reaches no request-bearing node.
func loginTokenTemplate(flow LoginFlow) (domain.APITemplate, bool) {
	maxSteps := flow.MaxSteps
	if maxSteps <= 0 {
		maxSteps = loginMaxStepsDefault
	}
	// A fixed seed makes the derivation deterministic; it only identifies the token
	// node structurally and sends no traffic.
	walker, err := engine.NewWalker(flow.Graph, 0)
	if err != nil {
		return domain.APITemplate{}, false
	}
	path, err := walker.Walk(flow.Start, maxSteps)
	if err != nil {
		return domain.APITemplate{}, false
	}
	tmplByNode := make(map[domain.ID]domain.ID, len(flow.Graph.Nodes))
	for _, n := range flow.Graph.Nodes {
		tmplByNode[n.ID] = n.APITemplateID
	}
	var found domain.APITemplate
	var ok bool
	for _, nodeID := range path {
		tmplID := tmplByNode[nodeID]
		if tmplID == "" {
			continue // pure-state node: no request
		}
		if tmpl, present := flow.Templates[tmplID]; present {
			found, ok = tmpl, true // keep walking: the LAST request-bearing node wins
		}
	}
	return found, ok
}

// isFormURLEncoded reports whether the template's Content-Type header marks an
// application/x-www-form-urlencoded body (case-insensitive on header name and value,
// tolerating a charset parameter).
func isFormURLEncoded(headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, "Content-Type") {
			return strings.Contains(strings.ToLower(v), "application/x-www-form-urlencoded")
		}
	}
	return false
}

// refreshGrantDrop is the set of form fields a refresh grant must not carry: the
// grant selector itself (rewritten to refresh_token), the password/
// authorization-code grant inputs that a refresh exchange does not use, and a
// refresh_token the LOGIN body itself carried (the paste-a-refresh-token path:
// the derived exchange sends only the freshly captured {{.refreshToken}} — a
// duplicate param breaks strict IdPs and would pin refresh to the stale paste).
var refreshGrantDrop = map[string]bool{
	"grant_type":    true,
	"username":      true,
	"password":      true,
	"code":          true,
	"refresh_token": true,
}

// rewriteToRefreshGrant rewrites a parsed login form into a refresh-grant body: it
// PREPENDS grant_type=refresh_token & refresh_token={{.refreshToken | urlquery}}, then
// appends every kept field (client_id/client_secret/scope/audience and any others),
// dropping the grant selector and the password/code inputs.
//
// The grant_type field is emitted literally (refresh_token is already form-safe). The
// refresh_token placeholder is routed through text/template's urlquery builtin so the
// CAPTURED refresh token is url-encoded at render time: an opaque / standard-base64
// token containing +, /, = or a space (which Render substitutes RAW) would otherwise
// corrupt the x-www-form-urlencoded body. urlquery calls url.QueryEscape, matching how
// the kept values below are encoded, so the token endpoint's url.ParseQuery decode
// recovers the exact original token. (Scoped to the refresh body this slice; the
// login {{.password}}/{{.username}} path is a separate follow-up.)
func rewriteToRefreshGrant(form url.Values) string {
	var b strings.Builder
	b.WriteString("grant_type=refresh_token&refresh_token={{.refreshToken | urlquery}}")
	// Deterministic order: sort the kept keys so the derived body is stable.
	keys := make([]string, 0, len(form))
	for k := range form {
		if refreshGrantDrop[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range form[k] {
			b.WriteByte('&')
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
	}
	return b.String()
}
