package scenariofile

import (
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// loginRefreshOverrideYAML is a login auth block carrying an EXPLICIT refresh
// override: a JSON-body login (which would NOT auto-derive a refresh grant) plus an
// auth.login.refresh sub-block naming the token endpoint and a form-encoded refresh
// body. buildLoginCredentials must thread the override onto the LoginFlowSpec.
const loginRefreshOverrideYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  strategy: login
  login:
    flow:
      - id: login
        request: POST /login
        body: '{"u":"svc"}'
    refresh:
      request: POST /oauth/token
      body: 'grant_type=refresh_token&refresh_token={{.refreshToken}}&client_id=c'
`

// TestExpandAuthLoginRefreshOverride pins that the explicit refresh override maps
// onto the LoginFlowSpec's RefreshRequest/RefreshBody, so the orchestrator can build
// the refresh transport from the operator's authored grant.
func TestExpandAuthLoginRefreshOverride(t *testing.T) {
	s, err := Parse([]byte(loginRefreshOverrideYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.LoginFlow == nil {
		t.Fatal("expanded login spec carries no login flow")
	}
	if spec.LoginFlow.RefreshRequest != "POST /oauth/token" {
		t.Errorf("login flow refresh request = %q, want POST /oauth/token", spec.LoginFlow.RefreshRequest)
	}
	want := "grant_type=refresh_token&refresh_token={{.refreshToken}}&client_id=c"
	if spec.LoginFlow.RefreshBody != want {
		t.Errorf("login flow refresh body = %q, want %q", spec.LoginFlow.RefreshBody, want)
	}
	if spec.CredentialPool.Strategy != domain.CredLogin {
		t.Errorf("strategy = %q, want login", spec.CredentialPool.Strategy)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded login spec with refresh override failed validation: %v", err)
	}
}

// TestExpandAuthLoginNoRefreshOverride pins that a login block WITHOUT a refresh
// sub-block leaves RefreshRequest/RefreshBody empty (auto-derive / re-login path),
// so the override is purely additive.
func TestExpandAuthLoginNoRefreshOverride(t *testing.T) {
	s, err := Parse([]byte(loginAuthYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.LoginFlow == nil {
		t.Fatal("expanded login spec carries no login flow")
	}
	if spec.LoginFlow.RefreshRequest != "" || spec.LoginFlow.RefreshBody != "" {
		t.Errorf("login flow refresh override = %q/%q, want empty (auto-derive)", spec.LoginFlow.RefreshRequest, spec.LoginFlow.RefreshBody)
	}
}

// TestExpandAuthLoginRefreshBodyOnly pins that the override's request line is
// OPTIONAL: a body-only refresh override (no request) is accepted, and the empty
// RefreshRequest defers the method/path to the login token endpoint at compile time.
func TestExpandAuthLoginRefreshBodyOnly(t *testing.T) {
	const bodyOnly = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  strategy: login
  login:
    flow:
      - id: login
        request: POST /oauth/token
        headers:
          Content-Type: application/x-www-form-urlencoded
        body: 'grant_type=password&username=u&password=p'
    refresh:
      body: 'grant_type=refresh_token&refresh_token={{.refreshToken}}'
`
	s, err := Parse([]byte(bodyOnly))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.LoginFlow.RefreshRequest != "" {
		t.Errorf("refresh request = %q, want empty (defaults to login endpoint)", spec.LoginFlow.RefreshRequest)
	}
	if spec.LoginFlow.RefreshBody == "" {
		t.Error("refresh body should carry the authored override body")
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("body-only refresh override failed validation: %v", err)
	}
}
