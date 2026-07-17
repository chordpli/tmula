package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// jsonLoginFlowWithOverride builds a JSON-body login flow — one whose auto-derive
// is NOT possible (no x-www-form-urlencoded grant_type body) — but which carries an
// EXPLICIT refresh override. It exercises the override's whole reason for existing:
// a non-OAuth2-form login can still get a real refresh transport.
func jsonLoginFlowWithOverride() LoginFlow {
	f := LoginFlow{
		Graph: domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "tlogin"}}},
		Templates: map[domain.ID]domain.APITemplate{
			"tlogin": {
				Method:          "POST",
				Path:            "/login",
				Headers:         map[string]string{"Content-Type": "application/json"},
				PayloadTemplate: `{"username":"u","password":"p"}`,
			},
		},
		Start:    "login",
		MaxSteps: 4,
		// The explicit override: a form-encoded refresh grant the operator authored
		// against a token endpoint the JSON login never exposed in a derivable shape.
		RefreshRequest: "POST /oauth/token",
		RefreshBody:    "grant_type=refresh_token&refresh_token={{.refreshToken}}&client_id=c",
	}
	return f
}

// TestRefreshTemplateOverrideWinsOverAutoDerive pins the headline feature: when a
// login carries an explicit refresh override, refreshTemplateFor SHORT-CIRCUITS the
// auto-derive gate — even a JSON-body login (which deriveRefreshTemplate refuses)
// gets a refresh template, built from the override's method/path/body.
func TestRefreshTemplateOverrideWinsOverAutoDerive(t *testing.T) {
	flow := jsonLoginFlowWithOverride()

	// Sanity: auto-derive alone would refuse this JSON-body login.
	if _, ok := deriveRefreshTemplate(flow); ok {
		t.Fatal("precondition: a JSON-body login must NOT auto-derive a refresh template")
	}

	tmpl, ok := refreshTemplateFor(flow)
	if !ok {
		t.Fatal("an explicit refresh override must produce a refresh template even when auto-derive cannot")
	}
	if tmpl.Method != "POST" || tmpl.Path != "/oauth/token" {
		t.Errorf("override method/path = %s %s, want POST /oauth/token (from RefreshRequest)", tmpl.Method, tmpl.Path)
	}
	// The override body is used verbatim, EXCEPT the bare {{.refreshToken}} placeholder
	// is routed through urlquery so an opaque token stays form-safe (same convention as
	// auto-derive).
	if !strings.Contains(tmpl.PayloadTemplate, "{{.refreshToken | urlquery}}") {
		t.Errorf("override body must route the refresh token through urlquery; got %q", tmpl.PayloadTemplate)
	}
	if !strings.Contains(tmpl.PayloadTemplate, "grant_type=refresh_token") {
		t.Errorf("override body lost its grant_type=refresh_token; got %q", tmpl.PayloadTemplate)
	}
	if !strings.Contains(tmpl.PayloadTemplate, "client_id=c") {
		t.Errorf("override body lost its client_id; got %q", tmpl.PayloadTemplate)
	}
}

// TestRefreshTemplateOverrideDefaultsToLoginEndpoint pins that RefreshRequest is
// OPTIONAL: when the override carries only a body, the method/path default to the
// login flow's token node, so a same-endpoint refresh grant needs only the body.
func TestRefreshTemplateOverrideDefaultsToLoginEndpoint(t *testing.T) {
	flow := jsonLoginFlowWithOverride()
	flow.RefreshRequest = "" // body-only override

	tmpl, ok := refreshTemplateFor(flow)
	if !ok {
		t.Fatal("a body-only refresh override must still produce a refresh template")
	}
	if tmpl.Method != "POST" || tmpl.Path != "/login" {
		t.Errorf("override method/path = %s %s, want POST /login (the login token node)", tmpl.Method, tmpl.Path)
	}
}

// TestRefreshTemplateOverridePrefersExplicitOverAutoDerive pins that even when a
// login WOULD auto-derive (an OAuth2 form grant), an explicit override still wins —
// the operator's authored body is authoritative, not the derived one.
func TestRefreshTemplateOverridePrefersExplicitOverAutoDerive(t *testing.T) {
	flow := oauthLoginFlow() // a derivable form grant
	flow.RefreshBody = "grant_type=refresh_token&refresh_token={{.refreshToken}}&audience=api"

	// Auto-derive alone would succeed here…
	if _, ok := deriveRefreshTemplate(flow); !ok {
		t.Fatal("precondition: the oauth flow should auto-derive")
	}
	// …but the override must win.
	tmpl, ok := refreshTemplateFor(flow)
	if !ok {
		t.Fatal("override should produce a template")
	}
	if !strings.Contains(tmpl.PayloadTemplate, "audience=api") {
		t.Errorf("override body should be used (audience=api), not the auto-derived one; got %q", tmpl.PayloadTemplate)
	}
}

// TestRefreshTemplateNoOverrideFallsBackToAutoDerive pins that with NO override an
// OAuth2 form login still auto-derives exactly as before — the override is purely
// additive and does not regress the auto path.
func TestRefreshTemplateNoOverrideFallsBackToAutoDerive(t *testing.T) {
	flow := oauthLoginFlow() // no override fields set
	tmpl, ok := refreshTemplateFor(flow)
	if !ok {
		t.Fatal("no override + an OAuth2 form login should auto-derive")
	}
	// The auto-derived body drops grant_type=password and prepends the refresh grant.
	if !strings.Contains(tmpl.PayloadTemplate, "grant_type=refresh_token&refresh_token={{.refreshToken | urlquery}}") {
		t.Errorf("auto-derive regressed: %q", tmpl.PayloadTemplate)
	}
}

// TestRefreshTemplateNeitherOverrideNorDerivable pins that a JSON login with NO
// override yields no refresh template (re-login fallback), unchanged behavior.
func TestRefreshTemplateNeitherOverrideNorDerivable(t *testing.T) {
	flow := jsonLoginFlowWithOverride()
	flow.RefreshRequest = ""
	flow.RefreshBody = "" // no override
	if _, ok := refreshTemplateFor(flow); ok {
		t.Error("a JSON login with no override must not produce a refresh template (re-login fallback)")
	}
}

// TestRefreshOverrideExchangeURLEncoded pins the end-to-end override path: a JSON
// login (non-derivable) with an explicit override produces a WORKING RefreshTokenFunc
// that exchanges the refresh token, and an opaque token containing +, /, = and a
// space round-trips intact through the override body's urlquery encoding.
func TestRefreshOverrideExchangeURLEncoded(t *testing.T) {
	const opaque = "aGVsbG8+d29y/bGQ= and+more" // contains + / = and a space
	var sawGrant, sawRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sawGrant = r.PostFormValue("grant_type")
		sawRefresh = r.PostFormValue("refresh_token") // url.ParseQuery-decoded
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "refresh_token": "refresh-2"})
	}))
	defer srv.Close()

	flow := jsonLoginFlowWithOverride()
	// Point the override at the test endpoint and give it a form-encoded grant body.
	flow.RefreshRequest = "POST /oauth/token"
	flow.RefreshBody = "grant_type=refresh_token&refresh_token={{.refreshToken}}&client_id=c"

	refreshTmpl, ok := refreshTemplateFor(flow)
	if !ok {
		t.Fatal("expected the override to produce a refresh template")
	}
	// The override body must carry a form Content-Type so the endpoint parses it; the
	// override builder copies the login node's headers, which here are JSON, so the
	// override must set a form Content-Type itself. Assert it is form-encoded.
	if ct := refreshTmpl.Headers["Content-Type"]; !strings.Contains(strings.ToLower(ct), "application/x-www-form-urlencoded") {
		t.Fatalf("override refresh template Content-Type = %q, want x-www-form-urlencoded", ct)
	}

	templates := map[domain.ID]domain.APITemplate{refreshTmpl.ID: refreshTmpl}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, templates, load.WithGuard(guardFor(t, srv.URL)))
	rf := NewRefreshTokenFunc(runner, refreshTmpl, 1)

	cur := domain.Credential{Subject: "alice", Secret: "access-1", Refresh: opaque}
	got, err := rf(context.Background(), 0, cur)
	if err != nil {
		t.Fatalf("override refresh exchange: %v", err)
	}
	if sawGrant != "refresh_token" {
		t.Errorf("token endpoint saw grant_type = %q, want refresh_token", sawGrant)
	}
	if sawRefresh != opaque {
		t.Errorf("token endpoint decoded refresh_token = %q, want %q (exact opaque token via urlquery)", sawRefresh, opaque)
	}
	if got.Secret != "access-2" {
		t.Errorf("rotated access token = %q, want access-2", got.Secret)
	}
}
