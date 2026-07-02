package importer

import (
	"strings"
	"testing"
	"text/template"

	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// findStep returns the first flow step whose request matches, or nil.
func findStep(s scenariofile.Scenario, request string) *scenariofile.Step {
	for i := range s.Flow {
		if s.Flow[i].Request == request {
			return &s.Flow[i]
		}
	}
	return nil
}

// assertTemplateSafe fails if a string is not a renderable Go text/template — the
// engine the run path renders login/header values through. A bare REPLACE_ME_*
// literal is safe; a {{REPLACE_ME}} form would parse as an undefined function and
// break the run, so this guards the placeholders the importer emits.
func assertTemplateSafe(t *testing.T, label, body string) {
	t.Helper()
	if !strings.Contains(body, "{{") {
		return // brace-free: rendered verbatim, always safe
	}
	// Parse with the same function set the run path renders with (load.apply), so a
	// derived value using a run-path func (e.g. basicAuth) parses here exactly as there.
	if _, err := template.New(label).Option("missingkey=error").Funcs(load.TemplateFuncs()).Parse(body); err != nil {
		t.Errorf("%s is not a renderable template (would break the run): %q: %v", label, body, err)
	}
}

// TestSecurityOAuth2Password derives a login block from an oauth2 password flow:
// the tokenUrl becomes a single POST login step with a form-urlencoded
// grant_type=password body carrying REPLACE_ME placeholders, the scope is
// per-user, the token capture is left empty (E1 auto-detects access_token), and
// the secured operation carries an Authorization: Bearer header.
func TestSecurityOAuth2Password(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    oauth:
      type: oauth2
      flows:
        password:
          tokenUrl: /oauth/token
          scopes:
            read: read access
security:
  - oauth: [read]
paths:
  /items:
    get:
      operationId: listItems
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth == nil {
		t.Fatal("expected an auth block, got nil")
	}
	if s.Auth.Strategy != "login" {
		t.Errorf("strategy = %q, want login", s.Auth.Strategy)
	}
	if s.Auth.Login == nil || len(s.Auth.Login.Flow) != 1 {
		t.Fatalf("login flow = %+v, want a single step", s.Auth.Login)
	}
	step := s.Auth.Login.Flow[0]
	if step.Request != "POST /oauth/token" {
		t.Errorf("login request = %q, want POST /oauth/token", step.Request)
	}
	if ct := step.Headers["Content-Type"]; ct != "application/x-www-form-urlencoded" {
		t.Errorf("login Content-Type = %q, want application/x-www-form-urlencoded", ct)
	}
	if !strings.Contains(step.Body, "grant_type=password") {
		t.Errorf("login body = %q, want grant_type=password", step.Body)
	}
	if !strings.Contains(step.Body, "REPLACE_ME_PASSWORD") {
		t.Errorf("login body = %q, want a REPLACE_ME_PASSWORD placeholder", step.Body)
	}
	if !strings.Contains(step.Body, "REPLACE_ME_USERNAME") {
		t.Errorf("login body = %q, want a REPLACE_ME_USERNAME placeholder", step.Body)
	}
	// The placeholders must not break the run: the body is rendered as a template.
	assertTemplateSafe(t, "password login body", step.Body)
	if s.Auth.Login.Capture.Token != "" {
		t.Errorf("token capture = %q, want empty (E1 auto-detects access_token)", s.Auth.Login.Capture.Token)
	}
	if s.Auth.Login.Scope != "per-user" {
		t.Errorf("scope = %q, want per-user", s.Auth.Login.Scope)
	}
	// The secured operation carries the bearer header.
	get := findStep(s, "GET /items")
	if get == nil {
		t.Fatal("no GET /items step")
	}
	if got := get.Headers["Authorization"]; got != "Bearer {{.token}}" {
		t.Errorf("GET /items Authorization = %q, want Bearer {{.token}}", got)
	}
	// The whole thing must expand into a runnable spec (after the user fills the secret;
	// REPLACE_ME placeholders are inert template-free strings so expand succeeds).
	if _, err := scenariofile.Expand(s); err != nil {
		t.Errorf("expand imported scenario: %v", err)
	}
}

// TestSecurityOAuth2ClientCredentials derives a shared-scope login from a
// clientCredentials flow: a form body grant_type=client_credentials with
// REPLACE_ME client id/secret, scope shared, empty token capture.
func TestSecurityOAuth2ClientCredentials(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    svc:
      type: oauth2
      flows:
        clientCredentials:
          tokenUrl: https://auth.example.com/token
security:
  - svc: []
paths:
  /jobs:
    post:
      operationId: createJob
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "login" || s.Auth.Login == nil {
		t.Fatalf("auth = %+v, want a login strategy", s.Auth)
	}
	step := s.Auth.Login.Flow[0]
	if !strings.Contains(step.Body, "grant_type=client_credentials") {
		t.Errorf("login body = %q, want grant_type=client_credentials", step.Body)
	}
	if !strings.Contains(step.Body, "REPLACE_ME_CLIENT_ID") || !strings.Contains(step.Body, "REPLACE_ME_CLIENT_SECRET") {
		t.Errorf("login body = %q, want REPLACE_ME_CLIENT_ID and REPLACE_ME_CLIENT_SECRET", step.Body)
	}
	assertTemplateSafe(t, "clientCredentials login body", step.Body)
	if step.Request != "POST /token" {
		t.Errorf("login request = %q, want POST /token (path of the absolute tokenUrl)", step.Request)
	}
	if s.Auth.Login.Scope != "shared" {
		t.Errorf("scope = %q, want shared", s.Auth.Login.Scope)
	}
	if s.Auth.Login.Capture.Token != "" {
		t.Errorf("token capture = %q, want empty", s.Auth.Login.Capture.Token)
	}
	post := findStep(s, "POST /jobs")
	if post == nil || post.Headers["Authorization"] != "Bearer {{.token}}" {
		t.Errorf("POST /jobs auth header = %+v, want Bearer {{.token}}", post)
	}
}

// TestSecurityHTTPBearerWithLoginOp derives a login from a discoverable login
// operation when the scheme is http bearer (no oauth2 tokenUrl). The login step
// is that operation's method+path with a body templated from its requestBody,
// the password-like field marked REPLACE_ME_PASSWORD, and the token capture left
// empty.
func TestSecurityHTTPBearerWithLoginOp(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    bearer:
      type: http
      scheme: bearer
security:
  - bearer: []
paths:
  /login:
    post:
      operationId: login
      requestBody:
        content:
          application/json:
            example: { "username": "alice", "password": "s3cret" }
  /me:
    get:
      operationId: me
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "login" || s.Auth.Login == nil {
		t.Fatalf("auth = %+v, want a login strategy from the /login op", s.Auth)
	}
	step := s.Auth.Login.Flow[0]
	if step.Request != "POST /login" {
		t.Errorf("login request = %q, want POST /login (the discovered login op)", step.Request)
	}
	if !strings.Contains(step.Body, "REPLACE_ME_PASSWORD") {
		t.Errorf("login body = %q, want the password field marked REPLACE_ME_PASSWORD", step.Body)
	}
	if strings.Contains(step.Body, "s3cret") {
		t.Errorf("login body = %q, must not leak the example password", step.Body)
	}
	if s.Auth.Login.Capture.Token != "" {
		t.Errorf("token capture = %q, want empty (auto-detect)", s.Auth.Login.Capture.Token)
	}
	// The non-login secured op carries the bearer header; the login op itself does not.
	me := findStep(s, "GET /me")
	if me == nil || me.Headers["Authorization"] != "Bearer {{.token}}" {
		t.Errorf("GET /me auth header = %+v, want Bearer {{.token}}", me)
	}
	login := findStep(s, "POST /login")
	if login != nil && login.Headers["Authorization"] != "" {
		t.Errorf("login op should NOT carry a bearer header, got %q", login.Headers["Authorization"])
	}
}

// TestSecurityHTTPBearerNoLoginOp emits a pool placeholder when the scheme is
// http bearer but no login operation is discoverable: a single REPLACE_ME_TOKEN
// user, no login flow, and the secured ops still carry the bearer header.
func TestSecurityHTTPBearerNoLoginOp(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    bearer:
      type: http
      scheme: bearer
security:
  - bearer: []
paths:
  /items:
    get:
      operationId: listItems
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth == nil {
		t.Fatal("expected a pool placeholder, got nil auth")
	}
	if s.Auth.Strategy != "pool" {
		t.Errorf("strategy = %q, want pool", s.Auth.Strategy)
	}
	if s.Auth.Login != nil {
		t.Errorf("must not invent a login flow, got %+v", s.Auth.Login)
	}
	if len(s.Auth.Users) != 1 || s.Auth.Users[0].Token != "REPLACE_ME_TOKEN" {
		t.Errorf("users = %+v, want a single REPLACE_ME_TOKEN entry", s.Auth.Users)
	}
	get := findStep(s, "GET /items")
	if get == nil || get.Headers["Authorization"] != "Bearer {{.token}}" {
		t.Errorf("GET /items auth header = %+v, want Bearer {{.token}}", get)
	}
}

// TestSecurityAPIKeyHeader emits a pool placeholder for an apiKey header scheme:
// the named header carries {{.token}} on secured ops and the pool entry is a
// REPLACE_ME_API_KEY placeholder. No login flow is invented.
func TestSecurityAPIKeyHeader(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    apiKey:
      type: apiKey
      in: header
      name: X-API-Key
security:
  - apiKey: []
paths:
  /items:
    get:
      operationId: listItems
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "pool" {
		t.Fatalf("auth = %+v, want a pool strategy", s.Auth)
	}
	if s.Auth.Login != nil {
		t.Errorf("must not invent a login flow for an apiKey, got %+v", s.Auth.Login)
	}
	if len(s.Auth.Users) != 1 || s.Auth.Users[0].Token != "REPLACE_ME_API_KEY" {
		t.Errorf("users = %+v, want a single REPLACE_ME_API_KEY entry", s.Auth.Users)
	}
	get := findStep(s, "GET /items")
	if get == nil {
		t.Fatal("no GET /items step")
	}
	if got := get.Headers["X-API-Key"]; got != "{{.token}}" {
		t.Errorf("X-API-Key header = %q, want {{.token}}", got)
	}
	if get.Headers["Authorization"] != "" {
		t.Errorf("apiKey scheme must not emit an Authorization header, got %q", get.Headers["Authorization"])
	}
}

// TestSecurityNoneIsBackwardCompatible is the backward-compat control: a doc with
// no security scheme and no login op produces the SAME scenario as before — no
// auth block and no injected headers.
func TestSecurityNoneIsBackwardCompatible(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  /items:
    get:
      operationId: listItems
  /health:
    get: {}
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth != nil {
		t.Errorf("no security scheme should emit no auth, got %+v", s.Auth)
	}
	for _, st := range s.Flow {
		if len(st.Headers) != 0 {
			t.Errorf("step %q should carry no injected headers, got %+v", st.ID, st.Headers)
		}
	}
}

// TestSecurityMalformedSchemeIsResilient ensures a partial/malformed security
// scheme does not break the whole import: the flow still imports, just without a
// usable auth block.
func TestSecurityMalformedSchemeIsResilient(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    broken:
      type: oauth2
      flows: {}
security:
  - broken: []
paths:
  /items:
    get:
      operationId: listItems
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI must not fail on a malformed scheme: %v", err)
	}
	if len(s.Flow) != 1 {
		t.Fatalf("flow = %d, want 1 (import survives a malformed scheme)", len(s.Flow))
	}
	// A flows-less oauth2 scheme yields no derivable login; emit no auth rather than
	// a broken one.
	if s.Auth != nil && s.Auth.Strategy == "login" {
		t.Errorf("a flows-less oauth2 scheme must not yield a login block, got %+v", s.Auth)
	}
}

// TestSecurityHTTPBasic derives a pool block from an http basic scheme: the
// credential row is username (subject) + password (token) and secured operations
// carry an RFC 7617 Authorization header built by the run path's basicAuth
// template function. No login flow is invented — basic re-sends the credential
// on every request.
func TestSecurityHTTPBasic(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    basic:
      type: http
      scheme: basic
security:
  - basic: []
paths:
  /items:
    get:
      operationId: listItems
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "pool" {
		t.Fatalf("auth = %+v, want a pool strategy", s.Auth)
	}
	if s.Auth.Login != nil {
		t.Errorf("must not invent a login flow for http basic, got %+v", s.Auth.Login)
	}
	if len(s.Auth.Users) != 1 ||
		s.Auth.Users[0].Subject != "REPLACE_ME_USERNAME" ||
		s.Auth.Users[0].Token != "REPLACE_ME_PASSWORD" {
		t.Errorf("users = %+v, want a single REPLACE_ME_USERNAME/REPLACE_ME_PASSWORD entry", s.Auth.Users)
	}
	get := findStep(s, "GET /items")
	if get == nil || get.Headers["Authorization"] != "Basic {{basicAuth .subject .token}}" {
		t.Errorf("GET /items auth header = %+v, want Basic {{basicAuth .subject .token}}", get)
	}
	if get != nil {
		assertTemplateSafe(t, "basic auth header", get.Headers["Authorization"])
	}
}

// TestSecurityAPIKeyQuery emits a pool placeholder for an apiKey-in-query scheme:
// the named query parameter is appended to each secured operation's request path
// as a space-free {{.token|urlquery}} template (parseRequest demands a two-field
// "METHOD /path" line, so the pipe carries no spaces).
func TestSecurityAPIKeyQuery(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    apiKey:
      type: apiKey
      in: query
      name: api_key
security:
  - apiKey: []
paths:
  /items:
    get:
      operationId: listItems
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "pool" {
		t.Fatalf("auth = %+v, want a pool strategy", s.Auth)
	}
	if len(s.Auth.Users) != 1 || s.Auth.Users[0].Token != "REPLACE_ME_API_KEY" {
		t.Errorf("users = %+v, want a single REPLACE_ME_API_KEY entry", s.Auth.Users)
	}
	get := findStep(s, "GET /items?api_key={{.token|urlquery}}")
	if get == nil {
		t.Fatalf("no step with the api_key query appended; flow = %+v", s.Flow)
	}
	if len(get.Headers) != 0 {
		t.Errorf("a query apiKey must not inject a header, got %+v", get.Headers)
	}
	assertTemplateSafe(t, "query apiKey path", get.Request)
	if _, err := scenariofile.Expand(s); err != nil {
		t.Errorf("expand imported scenario: %v", err)
	}
}

// TestSecurityAPIKeyCookie emits a pool placeholder for an apiKey-in-cookie
// scheme: secured operations carry a Cookie header pairing the named cookie with
// {{.token}}.
func TestSecurityAPIKeyCookie(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
components:
  securitySchemes:
    apiKey:
      type: apiKey
      in: cookie
      name: session
security:
  - apiKey: []
paths:
  /items:
    get:
      operationId: listItems
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "pool" {
		t.Fatalf("auth = %+v, want a pool strategy", s.Auth)
	}
	get := findStep(s, "GET /items")
	if get == nil || get.Headers["Cookie"] != "session={{.token}}" {
		t.Errorf("GET /items Cookie header = %+v, want session={{.token}}", get)
	}
	if get != nil {
		assertTemplateSafe(t, "cookie apiKey header", get.Headers["Cookie"])
	}
}
