package importer

import (
	"net/url"
	"strings"

	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// extractHARAuth auto-extracts the auth captured in a recorded HAR and folds it into
// the scenario in place. It is the E3 "Easy Auth" on-ramp: a developer logs in once
// in the browser, exports the HAR, and tmula lifts the live credential and (where
// present) the login request out of it — zero auth authoring.
//
// It does three things, all best-effort (any failure leaves the scenario as-is, so a
// HAR with no auth imports byte-for-byte as before):
//
//  1. Captures the live token — the most common Authorization bearer/scheme value, or
//     an auth cookie (name contains session/token/jwt/auth/sid) — across the entries.
//  2. Detects the login request — the entry the login classifier (shared with the
//     OpenAPI path) recognizes, or whose JSON response carries a token (via E1's
//     load.DetectCredential), or the one immediately preceding the first
//     Authorization-bearing request.
//  3. Emits a scenariofile.Auth: a refreshable "login" block when a login request is
//     found (capture left empty so E1 auto-detects the token), else a "pool" with the
//     captured token as a single inline secret; and rewrites every replayed step that
//     carried the captured token to Authorization: Bearer {{.token}} so the run uses
//     the pool/login token rather than the stale captured literal.
//
// AD-011: a HAR-extracted token IS a real secret. It is never logged; it rides only
// as a pool entry secret (the scenariofile inline-users authoring carrier, mapped onto
// a domain.Credential whose Secret is json:"-" and masked at rest) — the same as a user
// pasting their own captured token.
//
// keptFor[i] is the flow index entry i produced (or -1 if it was dropped), so the
// extractor can rewrite the right steps and skip the login entry's own step.
func extractHARAuth(sc *scenariofile.Scenario, entries []harEntry, keptFor []int) {
	capt := captureHARToken(entries)
	if capt.token == "" {
		return // no Authorization header and no auth cookie: import unchanged
	}

	loginIdx, hasLogin := findHARLoginEntry(entries, capt)

	// Rewrite every replayed step that carried the captured token so it uses the
	// pool/login token template instead of the stale literal. The login entry's own
	// step (if it was kept) is skipped — it mints the token, it does not consume one.
	rewriteHARAuthSteps(sc, entries, keptFor, capt, loginIdx, hasLogin)

	switch {
	case hasLogin:
		sc.Auth = harLoginAuth(entries[loginIdx])
	default:
		sc.Auth = harPoolAuth(capt.token)
	}
}

// harCapture is the captured live credential: the bare token secret, the header it
// rode in (Authorization, or "" for a cookie) and the scheme prefix ("Bearer", or ""
// for a raw value / cookie) so the rewrite can reproduce the same header shape.
type harCapture struct {
	token  string // the bare secret (no scheme prefix)
	header string // the request header it rode in, e.g. "Authorization" ("" => cookie)
	scheme string // the auth scheme, e.g. "Bearer" ("" => raw value or cookie)
}

// harTokenTally counts how often a captured token appears and remembers the order it
// was first seen, so the pick is deterministic (most common, ties to first-seen).
type harTokenTally struct {
	cap   harCapture
	count int
	order int
}

// captureHARToken scans the entries' request headers for an Authorization credential,
// falling back to an auth cookie, and returns the most common one (ties resolve to the
// first seen, which is stable for a recorded session). An Authorization header always
// beats a cookie, matching E1's "an explicit body token beats a cookie" precedence.
func captureHARToken(entries []harEntry) harCapture {
	bearer := map[string]*harTokenTally{}  // keyed by bare token
	cookies := map[string]*harTokenTally{} // keyed by bare token
	n := 0
	tally := func(m map[string]*harTokenTally, c harCapture) {
		if c.token == "" {
			return
		}
		if s, ok := m[c.token]; ok {
			s.count++
			return
		}
		m[c.token] = &harTokenTally{cap: c, count: 1, order: n}
		n++
	}

	for _, e := range entries {
		if c, ok := authHeaderCapture(e.Request.Headers); ok {
			tally(bearer, c)
		} else if c, ok := authCookieCapture(e); ok {
			tally(cookies, c)
		}
	}
	if c, ok := mostCommon(bearer); ok {
		return c
	}
	if c, ok := mostCommon(cookies); ok {
		return c
	}
	return harCapture{}
}

// mostCommon returns the highest-count capture, breaking ties by first-seen order so
// the choice is deterministic for a given HAR.
func mostCommon(m map[string]*harTokenTally) (harCapture, bool) {
	var best *harTokenTally
	for _, s := range m {
		if best == nil || s.count > best.count || (s.count == best.count && s.order < best.order) {
			best = s
		}
	}
	if best == nil {
		return harCapture{}, false
	}
	return best.cap, true
}

// authHeaderCapture pulls the credential from an Authorization request header. It
// splits a "<scheme> <token>" value (e.g. "Bearer abc") into scheme+token; a value
// with no space is taken whole as the token with an empty scheme.
func authHeaderCapture(headers []harNV) (harCapture, bool) {
	for _, h := range headers {
		if !strings.EqualFold(h.Name, "Authorization") {
			continue
		}
		v := strings.TrimSpace(h.Value)
		if v == "" {
			return harCapture{}, false
		}
		if i := strings.IndexByte(v, ' '); i > 0 {
			scheme, token := v[:i], strings.TrimSpace(v[i+1:])
			if token != "" {
				return harCapture{token: token, header: "Authorization", scheme: scheme}, true
			}
		}
		return harCapture{token: v, header: "Authorization"}, true
	}
	return harCapture{}, false
}

// authCookieCapture pulls a credential-shaped cookie (name contains
// session/token/jwt/auth/sid) from an entry's request Cookie header or its parsed
// HAR cookies array. The value becomes the captured token; the rewrite leaves the
// Cookie header in place (it already carries the literal), so header is left "".
func authCookieCapture(e harEntry) (harCapture, bool) {
	// HAR records request cookies both in the headers array (a "Cookie" header) and
	// in a parsed cookies array; check the structured array first, then the header.
	for _, c := range e.Request.Cookies {
		if isCredentialCookieName(c.Name) && c.Value != "" {
			return harCapture{token: c.Value}, true
		}
	}
	for _, h := range e.Request.Headers {
		if !strings.EqualFold(h.Name, "Cookie") {
			continue
		}
		for _, pair := range strings.Split(h.Value, ";") {
			name, value := splitCookiePair(pair)
			if value != "" && isCredentialCookieName(name) {
				return harCapture{token: value}, true
			}
		}
	}
	return harCapture{}, false
}

// credentialCookieNames mirrors E1's cookie-name heuristic (load.tokenFromCookies):
// a cookie whose name contains one of these is treated as carrying a session/bearer
// credential. Kept local because load does not export the list.
var credentialCookieNames = []string{"session", "token", "jwt", "auth", "sid"}

func isCredentialCookieName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, want := range credentialCookieNames {
		if strings.Contains(lower, want) {
			return true
		}
	}
	return false
}

// splitCookiePair splits a single "name=value" cookie pair, trimming surrounding
// whitespace.
func splitCookiePair(pair string) (name, value string) {
	pair = strings.TrimSpace(pair)
	if eq := strings.IndexByte(pair, '='); eq >= 0 {
		return strings.TrimSpace(pair[:eq]), strings.TrimSpace(pair[eq+1:])
	}
	return pair, ""
}

// findHARLoginEntry locates the entry that is the login/token call, in precedence
// order: (1) a path the shared login classifier recognizes; (2) an entry whose JSON
// response carries a detectable token (E1's load.DetectCredential), preferring one
// that yields the captured token; (3) the entry immediately preceding the first
// Authorization-bearing request. It returns the entry index and whether one was found.
func findHARLoginEntry(entries []harEntry, capt harCapture) (int, bool) {
	// (1) Classifier: the same login keywords the OpenAPI ordering uses, on the path.
	for i, e := range entries {
		if entryLooksLikeLogin(e) {
			return i, true
		}
	}
	// (2) Response carries a token. Prefer an entry whose detected token matches the
	// captured one (that is unambiguously the call that minted it); else the first
	// entry with any detectable token in a JSON response.
	firstTokenIdx := -1
	for i, e := range entries {
		body := e.Response.Content.Text
		if body == "" {
			continue
		}
		token, _ := load.DetectCredential([]byte(body), setCookieHeaders(e.Response.Headers))
		if token == "" {
			continue
		}
		if token == capt.token {
			return i, true
		}
		if firstTokenIdx < 0 {
			firstTokenIdx = i
		}
	}
	if firstTokenIdx >= 0 {
		return firstTokenIdx, true
	}
	// (3) The entry immediately before the first Authorization-bearing request: a
	// login that minted the token the next request starts carrying.
	for i, e := range entries {
		if _, ok := authHeaderCapture(e.Request.Headers); ok {
			if i > 0 {
				return i - 1, true
			}
			return 0, false // the very first request already carries auth: no login captured
		}
	}
	return 0, false
}

// entryLooksLikeLogin reports whether an entry's request path matches the shared
// login-keyword classifier.
func entryLooksLikeLogin(e harEntry) bool {
	method, path, ok := methodPathOf(e)
	if !ok {
		return false
	}
	return matchesAny(strings.ToLower(method+" "+path), loginKeywords...)
}

// setCookieHeaders pulls the Set-Cookie values out of a response's header array, so
// load.DetectCredential can fall back to a cookie token when the body has none.
func setCookieHeaders(headers []harNV) []string {
	var out []string
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Set-Cookie") {
			out = append(out, h.Value)
		}
	}
	return out
}

// rewriteHARAuthSteps rewrites every replayed step that originally carried the
// captured Authorization token so it uses Authorization: Bearer {{.token}} (the
// pool/login token) instead of the stale captured literal. The login entry's own
// step is left untouched — it mints the token rather than consuming one. A
// cookie-only capture leaves the Cookie header in place (it carries the literal the
// user owns) and injects nothing.
func rewriteHARAuthSteps(sc *scenariofile.Scenario, entries []harEntry, keptFor []int, capt harCapture, loginIdx int, hasLogin bool) {
	if capt.header == "" {
		return // cookie capture: nothing to template onto an Authorization header
	}
	value := "Bearer {{.token}}"
	if !strings.EqualFold(capt.scheme, "bearer") && capt.scheme != "" {
		// Preserve a non-bearer scheme (e.g. "Token", "JWT") as captured.
		value = capt.scheme + " {{.token}}"
	}
	for i, e := range entries {
		if hasLogin && i == loginIdx {
			continue // never inject the minted-token header onto the login call
		}
		idx := keptFor[i]
		if idx < 0 {
			continue // entry was dropped; no step to rewrite
		}
		hc, ok := authHeaderCapture(e.Request.Headers)
		if !ok || hc.token != capt.token {
			continue // this step did not carry the captured token
		}
		step := &sc.Flow[idx]
		if step.Headers == nil {
			step.Headers = map[string]string{}
		}
		step.Headers["Authorization"] = value
	}
}

// harLoginAuth emits a refreshable "login" auth block from the login entry: its
// request line and body become the login flow, capture is left EMPTY so E1
// auto-detects the token in the response, and no inline secret is carried.
func harLoginAuth(e harEntry) *scenariofile.Auth {
	method, path, _ := methodPathOf(e)
	var body string
	if e.Request.PostData != nil {
		body = e.Request.PostData.Text
	}
	return &scenariofile.Auth{
		Strategy: "login",
		Login: &scenariofile.AuthLogin{
			Flow: []scenariofile.Step{{
				ID:      "login",
				Request: strings.ToUpper(method) + " " + path,
				Body:    body,
			}},
			// Capture.Token intentionally empty — E1 auto-detects the token.
		},
	}
}

// harPoolAuth emits a "pool" auth block carrying the captured token as a single
// inline secret. AD-011: the token is a real captured secret; it rides only as the
// pool entry's Token (mapped onto a domain.Credential whose Secret is json:"-").
func harPoolAuth(token string) *scenariofile.Auth {
	return &scenariofile.Auth{
		Strategy: "pool",
		// The inline token here is the user's own captured secret — the same as
		// pasting it into an authored file. It is static and will expire; the P5
		// auth-expiry report note flags that.
		Users: []scenariofile.Credential{{Subject: "captured", Token: token}},
	}
}

// methodPathOf reduces a HAR entry to a "METHOD" + request path (path + query),
// matching how FromHAR builds a step's request line. It returns ok=false for a
// malformed URL or an unsafe path (so the caller does not build a broken request).
func methodPathOf(e harEntry) (method, path string, ok bool) {
	u, err := url.Parse(e.Request.URL)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	path = u.Path
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	if !safeRequestPath(path) {
		return "", "", false
	}
	return strings.ToUpper(e.Request.Method), path, true
}
