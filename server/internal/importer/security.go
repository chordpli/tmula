package importer

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// securityScheme is the subset of an OpenAPI components.securitySchemes entry the
// importer reads to derive auth. It spans the three families tmula can act on:
// oauth2 (with flows), http (bearer or basic), and apiKey (a static key). Anything
// else (mutualTLS, openIdConnect, http digest) parses but yields no derivable auth.
type securityScheme struct {
	Type   string                `json:"type"`   // oauth2 | http | apiKey | ...
	Scheme string                `json:"scheme"` // http: bearer | basic | ...
	In     string                `json:"in"`     // apiKey: header | query | cookie
	Name   string                `json:"name"`   // apiKey: the header/param name
	Flows  map[string]oauth2Flow `json:"flows"`  // oauth2: password | clientCredentials | ...
	// OpenIDConnectURL is the openIdConnect discovery document URL. The importer
	// stays offline (it never fetches it) but surfaces it as an advisory so the
	// operator can read the token_endpoint out of it for the OAuth2 route.
	OpenIDConnectURL string `json:"openIdConnectUrl"`
}

// oauth2Flow is one OAuth2 flow object: its tokenUrl is the login endpoint and its
// scopes name the grantable scopes (unused beyond presence today).
type oauth2Flow struct {
	TokenURL string            `json:"tokenUrl"`
	Scopes   map[string]string `json:"scopes"`
}

// securityRequirement is one entry in a top-level or per-operation `security`
// list: a map of scheme-name -> required scopes. An operation is secured under a
// scheme when any requirement names it.
type securityRequirement map[string][]string

// derivedAuth is the parsed-and-classified result the importer turns into a
// scenariofile.Auth and per-step header injection. schemeName is the security
// scheme an operation must satisfy; header/headerValue is what each secured
// operation's step carries.
type derivedAuth struct {
	auth        *scenariofile.Auth
	header      string // the header injected on secured operations (e.g. Authorization)
	headerValue string // the value template (e.g. "Bearer {{.token}}")
	// queryParam, when set, appends "<name>={{.token|urlquery}}" to each secured
	// operation's request path instead of injecting a header (the apiKey-in-query
	// scheme). The template is space-free because parseRequest demands a two-field
	// "METHOD /path" line.
	queryParam string
	// schemeNames are the scheme names this derivation covers, so a per-operation
	// security requirement naming any of them marks that operation secured.
	schemeNames map[string]bool
}

// deriveAuth reads the document's securitySchemes and security requirements and
// returns a scenariofile.Auth plus how to inject the auth header into secured
// operations. It is best-effort: a malformed or unsupported scheme yields a nil
// derivation (no auth) rather than an error, so a partial spec still imports.
//
// ops is the already-flattened operation list, used only to discover a login
// operation for the http-bearer case (the tokenUrl-less scheme).
func deriveAuth(doc openAPIDoc, ops []apiOp) *derivedAuth {
	schemes := doc.parseSecuritySchemes()
	if len(schemes) == 0 {
		return nil
	}

	// Pick the scheme to act on. Prefer one named by the top-level security default;
	// otherwise the first scheme we can classify. Most specs declare a single scheme.
	name, scheme, ok := pickScheme(schemes, doc.topLevelSecurity())
	if !ok {
		return nil
	}

	// The "managed-IdP — cannot self-issue" mint footgun is covered by the advisory
	// channel: deriveAuthAdvisories (called from FromOpenAPI over ALL schemes, not
	// just the picked one) emits mint-managed-idp for an openIdConnect scheme or an
	// oauth2 tokenUrl on a managed issuer host. The mint strategy can ONLY sign for
	// a key the operator holds (self-issued JWT, the M1 case).
	switch strings.ToLower(scheme.Type) {
	case "oauth2":
		return deriveOAuth2(name, scheme)
	case "http":
		if strings.EqualFold(scheme.Scheme, "bearer") {
			return deriveHTTPBearer(name, ops)
		}
		if strings.EqualFold(scheme.Scheme, "basic") {
			return deriveHTTPBasic(name)
		}
		return nil // http digest: no token flow to derive
	case "apikey":
		if apiKeyIn(scheme) != "" {
			return deriveAPIKey(name, scheme)
		}
		return nil // unnamed or unknown-location apiKey: nothing to inject
	default:
		return nil
	}
}

// deriveAuthAdvisories scans ALL declared security schemes (not just the one
// picked for derivation) for auth the importer cannot act on itself and returns
// import-time hints the UI surfaces:
//
//   - "openidconnect-discovery" (detail: the discovery URL) — the scheme is
//     openIdConnect; the importer stays offline, so the operator reads the
//     token_endpoint out of the discovery document for the OAuth2 route.
//   - "mint-managed-idp" (detail: the IdP host) — the token issuer is a managed
//     IdP (Auth0/Cognito/Firebase/Okta, or any openIdConnect issuer) whose
//     signing key the operator does NOT hold, so the mint strategy cannot work
//     (its tokens would be rejected). This is the #1 mint footgun.
//
// The scan is deterministic (sorted scheme order) and de-duplicated.
func deriveAuthAdvisories(schemes map[string]securityScheme) []domain.AuthAdvisory {
	var out []domain.AuthAdvisory
	seen := map[domain.AuthAdvisory]bool{}
	add := func(code, detail string) {
		a := domain.AuthAdvisory{Code: code, Detail: detail}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, name := range sortedKeys(schemes) {
		sc := schemes[name]
		switch strings.ToLower(sc.Type) {
		case "openidconnect":
			add("openidconnect-discovery", sc.OpenIDConnectURL)
			// Any openIdConnect issuer is a managed IdP from mint's point of view:
			// the operator holds no signing key for it.
			add("mint-managed-idp", hostOfURL(sc.OpenIDConnectURL))
		case "oauth2":
			for _, flow := range sc.Flows {
				if host := hostOfURL(flow.TokenURL); host != "" && managedIdPHost(host) {
					add("mint-managed-idp", host)
				}
			}
		}
	}
	return out
}

// managedIdPHost reports whether a token-issuer host belongs to a managed
// identity provider the operator cannot hold the signing key for.
func managedIdPHost(host string) bool {
	host = strings.ToLower(host)
	for _, suffix := range []string{
		".auth0.com",
		".okta.com",
		".oktapreview.com",
		".amazoncognito.com",
		".firebaseapp.com",
	} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	switch host {
	case "securetoken.google.com", "identitytoolkit.googleapis.com", "oauth2.googleapis.com":
		return true
	}
	// Cognito's issuer hosts: cognito-idp.<region>.amazonaws.com.
	return strings.HasPrefix(host, "cognito-idp.") && strings.HasSuffix(host, ".amazonaws.com")
}

// hostOfURL returns the host of an absolute URL, or "" for a relative or
// malformed value (a relative tokenUrl points at the target itself — self-hosted).
func hostOfURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.IsAbs() {
		return u.Hostname()
	}
	return ""
}

// pickScheme chooses which security scheme to derive auth from. A scheme named by
// the top-level security default wins (it is the document's stated default);
// otherwise the first scheme with a recognizable type is used. The returned name
// is the scheme's key, used to match per-operation security requirements.
func pickScheme(schemes map[string]securityScheme, top []securityRequirement) (string, securityScheme, bool) {
	// Prefer a scheme the top-level security default names.
	for _, req := range top {
		for schemeName := range req {
			if sc, ok := schemes[schemeName]; ok && classifiable(sc) {
				return schemeName, sc, true
			}
		}
	}
	// Otherwise the first classifiable scheme, in a stable (sorted) order so the
	// choice is deterministic across imports.
	for _, name := range sortedKeys(schemes) {
		if sc := schemes[name]; classifiable(sc) {
			return name, sc, true
		}
	}
	return "", securityScheme{}, false
}

// classifiable reports whether a scheme is one the importer can act on (and, for
// oauth2, carries a usable flow). A flows-less oauth2 scheme is not classifiable,
// so a malformed scheme falls through to "no auth" rather than a broken block.
func classifiable(sc securityScheme) bool {
	switch strings.ToLower(sc.Type) {
	case "oauth2":
		flowName, _ := pickOAuth2Flow(sc.Flows)
		return flowName != ""
	case "http":
		return strings.EqualFold(sc.Scheme, "bearer") || strings.EqualFold(sc.Scheme, "basic")
	case "apikey":
		return apiKeyIn(sc) != ""
	default:
		return false
	}
}

// apiKeyIn returns the normalized location of a usable apiKey scheme (header,
// query or cookie), or "" when the scheme is unnamed or somewhere the importer
// cannot inject.
func apiKeyIn(sc securityScheme) string {
	if sc.Name == "" {
		return ""
	}
	switch strings.ToLower(sc.In) {
	case "header", "query", "cookie":
		return strings.ToLower(sc.In)
	default:
		return ""
	}
}

// deriveOAuth2 builds a login block from an oauth2 scheme's flow. The tokenUrl is
// the login endpoint; the grant is form-urlencoded with REPLACE_ME placeholders
// for the secret(s). Password grants are per-user; clientCredentials are shared.
// The token capture is left empty so E1 auto-detects the standardized access_token.
func deriveOAuth2(name string, sc securityScheme) *derivedAuth {
	flowName, flow := pickOAuth2Flow(sc.Flows)
	tokenPath := requestPathOf(flow.TokenURL)
	if tokenPath == "" {
		return nil // no usable tokenUrl: cannot mint a token
	}

	// The REPLACE_ME_* placeholders are bare literals (no {{ }}): the login body is
	// rendered through Go text/template at run time, so a {{REPLACE_ME}} form would be
	// parsed as an (undefined) function call and break the login flow. A brace-free
	// literal both skips templating and is the value the user edits in place.
	var body, scope string
	switch flowName {
	case "clientCredentials":
		body = "grant_type=client_credentials&client_id=REPLACE_ME_CLIENT_ID&client_secret=REPLACE_ME_CLIENT_SECRET"
		scope = "shared"
	default: // password (and authorizationCode/implicit fall back to a password-shaped grant)
		body = "grant_type=password&username=REPLACE_ME_USERNAME&password=REPLACE_ME_PASSWORD"
		scope = "per-user"
	}

	login := &scenariofile.AuthLogin{
		Flow: []scenariofile.Step{{
			ID:      "login",
			Request: "POST " + tokenPath,
			Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			Body:    body,
		}},
		// Capture.Token is intentionally empty — E1 auto-detects the OAuth2 access_token.
		Scope: scope,
	}
	return &derivedAuth{
		auth:        &scenariofile.Auth{Strategy: "login", Login: login},
		header:      "Authorization",
		headerValue: "Bearer {{.token}}",
		schemeNames: map[string]bool{name: true},
	}
}

// deriveHTTPBearer handles an http bearer scheme, which carries no tokenUrl. If a
// login/token operation is discoverable in the flow (via the journeyStage login
// classifier), a login block is built from that operation; otherwise a pool
// placeholder is emitted (no endpoint is invented).
func deriveHTTPBearer(name string, ops []apiOp) *derivedAuth {
	if op, ok := findLoginOp(ops); ok {
		login := &scenariofile.AuthLogin{
			Flow: []scenariofile.Step{{
				ID:      "login",
				Request: strings.ToUpper(op.method) + " " + op.path,
				Body:    loginBodyFrom(op.op),
			}},
			// Capture.Token empty — auto-detect.
		}
		return &derivedAuth{
			auth:        &scenariofile.Auth{Strategy: "login", Login: login},
			header:      "Authorization",
			headerValue: "Bearer {{.token}}",
			schemeNames: map[string]bool{name: true},
		}
	}
	// No discoverable login op: emit a pool placeholder rather than inventing one.
	return &derivedAuth{
		auth: &scenariofile.Auth{
			Strategy: "pool",
			Users:    []scenariofile.Credential{{Subject: "tester", Token: "REPLACE_ME_TOKEN"}},
		},
		header:      "Authorization",
		headerValue: "Bearer {{.token}}",
		schemeNames: map[string]bool{name: true},
	}
}

// deriveHTTPBasic handles an http basic scheme: the credential row is username
// (subject) + password (token), and secured operations carry an RFC 7617
// Authorization header built by the run path's basicAuth template function. No
// login flow exists to derive — basic re-sends the credential on every request.
func deriveHTTPBasic(name string) *derivedAuth {
	return &derivedAuth{
		auth: &scenariofile.Auth{
			Strategy: "pool",
			Users:    []scenariofile.Credential{{Subject: "REPLACE_ME_USERNAME", Token: "REPLACE_ME_PASSWORD"}},
		},
		header:      "Authorization",
		headerValue: "Basic {{basicAuth .subject .token}}",
		schemeNames: map[string]bool{name: true},
	}
}

// deriveAPIKey handles an apiKey scheme: a pool placeholder for the key plus the
// key injected on secured operations where the scheme says it lives — the named
// header as {{.token}}, the named query parameter appended to the request path,
// or a Cookie header pairing the named cookie with {{.token}}. No login flow is
// invented — the key is static.
func deriveAPIKey(name string, sc securityScheme) *derivedAuth {
	d := &derivedAuth{
		auth: &scenariofile.Auth{
			Strategy: "pool",
			Users:    []scenariofile.Credential{{Subject: "tester", Token: "REPLACE_ME_API_KEY"}},
		},
		schemeNames: map[string]bool{name: true},
	}
	switch apiKeyIn(sc) {
	case "query":
		d.queryParam = sc.Name
	case "cookie":
		d.header = "Cookie"
		d.headerValue = sc.Name + "={{.token}}"
	default: // header
		d.header = sc.Name
		d.headerValue = "{{.token}}"
	}
	return d
}

// secures reports whether an operation requires one of the derived scheme(s).
// An operation is secured when its own `security` (or, absent that, the top-level
// default) names a covered scheme. An explicit empty `security: []` on an
// operation opts it out, even when a top-level default exists.
func (d *derivedAuth) secures(op operation, topLevel []securityRequirement) bool {
	reqs := topLevel
	if op.Security != nil { // present (even if empty) overrides the default
		reqs = *op.Security
	}
	for _, req := range reqs {
		for schemeName := range req {
			if d.schemeNames[schemeName] {
				return true
			}
		}
	}
	return false
}

// findLoginOp returns the first operation the journeyStage classifier ranks as an
// authentication step (login/oauth/token/session/register/...). It is the same
// classifier the ordering uses, reused here to discover a login endpoint.
func findLoginOp(ops []apiOp) (apiOp, bool) {
	for _, o := range ops {
		if journeyStage(o) == 0 {
			return o, true
		}
	}
	return apiOp{}, false
}

// loginBodyFrom builds a login request body from an operation's requestBody
// example, marking a password-like field as REPLACE_ME_PASSWORD so the secret is
// never carried from the spec. When no example is available it returns a minimal
// username/password JSON object with the placeholder.
func loginBodyFrom(op operation) string {
	ex := bodyExample(op)
	if ex == "" {
		return `{"username":"REPLACE_ME_USERNAME","password":"REPLACE_ME_PASSWORD"}`
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ex), &obj); err != nil {
		// Not a JSON object we can rewrite; return a safe minimal body rather than
		// leaking an arbitrary example that may carry a literal secret.
		return `{"username":"REPLACE_ME_USERNAME","password":"REPLACE_ME_PASSWORD"}`
	}
	for k := range obj {
		if isPasswordField(k) {
			obj[k] = json.RawMessage(`"REPLACE_ME_PASSWORD"`)
		}
	}
	if b, err := json.Marshal(obj); err == nil {
		return string(b)
	}
	return `{"username":"REPLACE_ME_USERNAME","password":"REPLACE_ME_PASSWORD"}`
}

// isPasswordField reports whether a requestBody field name looks like a secret
// the user must fill (password/secret/pass/pwd).
func isPasswordField(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "password") || strings.Contains(n, "passwd") ||
		n == "pass" || n == "pwd" || strings.Contains(n, "secret")
}

// pickOAuth2Flow chooses the flow to derive a token from, preferring a login-style
// grant (password) over a machine grant (clientCredentials) over the redirect
// grants (authorizationCode/implicit). It returns the canonical flow name and the
// flow, or ok=false when no flow with a tokenUrl is present.
func pickOAuth2Flow(flows map[string]oauth2Flow) (string, oauth2Flow) {
	for _, pref := range []string{"password", "clientCredentials", "authorizationCode", "implicit"} {
		if f, ok := flows[pref]; ok && requestPathOf(f.TokenURL) != "" {
			return pref, f
		}
	}
	return "", oauth2Flow{}
}

// requestPathOf reduces a tokenUrl (which may be absolute or a bare path) to a
// usable request path. An absolute URL keeps only its path (the run targets the
// scenario's base URL); a relative one is returned as-is. An empty or malformed
// value yields "".
func requestPathOf(tokenURL string) string {
	tokenURL = strings.TrimSpace(tokenURL)
	if tokenURL == "" {
		return ""
	}
	if u, err := url.Parse(tokenURL); err == nil && u.IsAbs() {
		if u.Path == "" {
			return ""
		}
		return u.Path
	}
	if !strings.HasPrefix(tokenURL, "/") {
		tokenURL = "/" + tokenURL
	}
	if !safeRequestPath(tokenURL) {
		return ""
	}
	return tokenURL
}

// parseSecuritySchemes pulls components.securitySchemes into typed schemes, best-
// effort: a scheme that fails to unmarshal is skipped, not fatal.
func (d openAPIDoc) parseSecuritySchemes() map[string]securityScheme {
	out := make(map[string]securityScheme, len(d.Components.SecuritySchemes))
	for name, raw := range d.Components.SecuritySchemes {
		var sc securityScheme
		if err := json.Unmarshal(raw, &sc); err != nil {
			continue
		}
		out[name] = sc
	}
	return out
}

// topLevelSecurity parses the document's top-level `security` default.
func (d openAPIDoc) topLevelSecurity() []securityRequirement {
	return parseSecurity(d.Security)
}

// parseSecurity decodes a raw `security` list into requirements, best-effort.
func parseSecurity(raw json.RawMessage) []securityRequirement {
	if len(raw) == 0 {
		return nil
	}
	var reqs []securityRequirement
	if err := json.Unmarshal(raw, &reqs); err != nil {
		return nil
	}
	return reqs
}

// sortedKeys returns a scheme map's keys in sorted order for a deterministic pick.
func sortedKeys(m map[string]securityScheme) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// small n: insertion-sort keeps it dependency-free and simple
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
