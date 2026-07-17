package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/safety"
)

// oauthLoginFlow builds a single-request OAuth2 password-grant login flow whose
// token POST body is application/x-www-form-urlencoded and carries grant_type.
func oauthLoginFlow() LoginFlow {
	return LoginFlow{
		Graph: domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "token", APITemplateID: "ttoken"}}},
		Templates: map[domain.ID]domain.APITemplate{
			"ttoken": {
				Method:          "POST",
				Path:            "/oauth/token",
				Headers:         map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
				PayloadTemplate: "grant_type=password&username={{.username}}&password={{.password}}&client_id=c&client_secret=s&scope=read",
			},
		},
		Start:    "token",
		MaxSteps: 4,
	}
}

// TestDeriveRefreshTemplateFromPasswordGrant pins test 6 (positive): a
// grant_type=password form-encoded login derives a refresh template that drops
// grant_type/username/password, keeps client_id/client_secret/scope, and prepends
// grant_type=refresh_token & refresh_token={{.refreshToken}}.
func TestDeriveRefreshTemplateFromPasswordGrant(t *testing.T) {
	flow := oauthLoginFlow()
	flow.Templates["ttoken"] = func() domain.APITemplate {
		tm := flow.Templates["ttoken"]
		tm.PayloadTemplate = "grant_type=password&username=u&password=p&client_id=c&client_secret=s&scope=x"
		return tm
	}()

	tmpl, ok := deriveRefreshTemplate(flow)
	if !ok {
		t.Fatal("a form-encoded grant_type=password login should derive a refresh template")
	}
	if tmpl.Method != "POST" || tmpl.Path != "/oauth/token" {
		t.Errorf("derived method/path = %s %s, want POST /oauth/token (reuse login node)", tmpl.Method, tmpl.Path)
	}
	if ct := tmpl.Headers["Content-Type"]; ct != "application/x-www-form-urlencoded" {
		t.Errorf("derived Content-Type = %q, want application/x-www-form-urlencoded (reuse login headers)", ct)
	}
	form, err := url.ParseQuery(tmpl.PayloadTemplate)
	if err != nil {
		t.Fatalf("derived body is not a parseable form: %v (%q)", err, tmpl.PayloadTemplate)
	}
	if got := form.Get("grant_type"); got != "refresh_token" {
		t.Errorf("derived grant_type = %q, want refresh_token", got)
	}
	if got := form.Get("refresh_token"); got != "{{.refreshToken | urlquery}}" {
		t.Errorf("derived refresh_token = %q, want {{.refreshToken | urlquery}} (urlquery keeps an opaque token form-safe)", got)
	}
	if _, ok := form["username"]; ok {
		t.Error("derived body still carries username (should be dropped)")
	}
	if _, ok := form["password"]; ok {
		t.Error("derived body still carries password (should be dropped)")
	}
	if form.Get("client_id") != "c" || form.Get("client_secret") != "s" || form.Get("scope") != "x" {
		t.Errorf("derived body lost a kept field: client_id=%q client_secret=%q scope=%q (want c/s/x)",
			form.Get("client_id"), form.Get("client_secret"), form.Get("scope"))
	}
	// The original grant_type=password must not survive.
	if c := strings.Count(tmpl.PayloadTemplate, "grant_type="); c != 1 {
		t.Errorf("derived body has %d grant_type fields, want exactly 1 (the rewritten refresh_token grant)", c)
	}
}

// TestDeriveRefreshTemplateNotDerivable pins test 6 (negative): a JSON-body login
// (not form-encoded) and a form login WITHOUT grant_type both return not-derivable,
// so no RefreshTokenFunc is wired and Refresh re-logins.
func TestDeriveRefreshTemplateNotDerivable(t *testing.T) {
	// JSON body login.
	jsonFlow := LoginFlow{
		Graph: domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
		Templates: map[domain.ID]domain.APITemplate{
			"t": {Method: "POST", Path: "/login", Headers: map[string]string{"Content-Type": "application/json"}, PayloadTemplate: `{"username":"u","password":"p"}`},
		},
		Start: "login", MaxSteps: 4,
	}
	if _, ok := deriveRefreshTemplate(jsonFlow); ok {
		t.Error("a JSON-body login must NOT derive a refresh template (not an OAuth2 form grant)")
	}

	// Form body but no grant_type.
	formNoGrant := LoginFlow{
		Graph: domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
		Templates: map[domain.ID]domain.APITemplate{
			"t": {Method: "POST", Path: "/login", Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, PayloadTemplate: "username=u&password=p"},
		},
		Start: "login", MaxSteps: 4,
	}
	if _, ok := deriveRefreshTemplate(formNoGrant); ok {
		t.Error("a form login with no grant_type must NOT derive a refresh template")
	}
}

// TestNewRefreshTokenFuncExchanges pins test 7: against an httptest token endpoint,
// the func renders the refresh template with the current credential, POSTs through
// the guarded runner, and captures the rotated access token, refresh token and
// expires_in.
func TestNewRefreshTokenFuncExchanges(t *testing.T) {
	var sawGrant, sawRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sawGrant = r.PostFormValue("grant_type")
		sawRefresh = r.PostFormValue("refresh_token")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-2",
			"refresh_token": "refresh-2",
			"expires_in":    120,
		})
	}))
	defer srv.Close()

	flow := oauthLoginFlow()
	refreshTmpl, ok := deriveRefreshTemplate(flow)
	if !ok {
		t.Fatal("expected the oauth flow to derive a refresh template")
	}
	templates := map[domain.ID]domain.APITemplate{refreshTmpl.ID: refreshTmpl}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, templates, load.WithGuard(guardFor(t, srv.URL)))
	rf := NewRefreshTokenFunc(runner, refreshTmpl, 1)

	cur := domain.Credential{Subject: "alice", Secret: "access-1", Refresh: "refresh-1", ExpiresIn: time.Minute}
	got, err := rf(context.Background(), 0, cur)
	if err != nil {
		t.Fatalf("refresh exchange: %v", err)
	}
	if sawGrant != "refresh_token" {
		t.Errorf("token endpoint saw grant_type = %q, want refresh_token", sawGrant)
	}
	if sawRefresh != "refresh-1" {
		t.Errorf("token endpoint saw refresh_token = %q, want refresh-1 (the current credential's refresh token)", sawRefresh)
	}
	if got.Secret != "access-2" {
		t.Errorf("rotated access token = %q, want access-2", got.Secret)
	}
	if got.Refresh != "refresh-2" {
		t.Errorf("rotated refresh token = %q, want refresh-2", got.Refresh)
	}
	if got.ExpiresIn != 2*time.Minute {
		t.Errorf("rotated expiry = %s, want 2m (from expires_in=120)", got.ExpiresIn)
	}
	if got.Subject != "alice" {
		t.Errorf("subject = %q, want alice (preserved from the current credential)", got.Subject)
	}
}

// TestNewRefreshTokenFuncCarriesForwardRefresh pins test 5: when the token endpoint
// rotates only the access token and OMITS a new refresh_token, the func carries
// forward the current credential's refresh token (does not blank it).
func TestNewRefreshTokenFuncCarriesForwardRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// No refresh_token in the response — a server that does not rotate it.
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "expires_in": 60})
	}))
	defer srv.Close()

	flow := oauthLoginFlow()
	refreshTmpl, _ := deriveRefreshTemplate(flow)
	templates := map[domain.ID]domain.APITemplate{refreshTmpl.ID: refreshTmpl}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, templates, load.WithGuard(guardFor(t, srv.URL)))
	rf := NewRefreshTokenFunc(runner, refreshTmpl, 1)

	cur := domain.Credential{Subject: "alice", Secret: "access-1", Refresh: "refresh-1"}
	got, err := rf(context.Background(), 0, cur)
	if err != nil {
		t.Fatalf("refresh exchange: %v", err)
	}
	if got.Secret != "access-2" {
		t.Errorf("access token = %q, want access-2", got.Secret)
	}
	if got.Refresh != "refresh-1" {
		t.Errorf("refresh token = %q, want refresh-1 carried forward (response omitted refresh_token)", got.Refresh)
	}
}

// TestNewRefreshTokenFuncNoAccessTokenErrors pins that a refresh response with no
// access token is a loud error (mirroring NewLoginTokenFunc's no-token error),
// rather than handing back a credential that authenticates as nobody.
func TestNewRefreshTokenFuncNoAccessTokenErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	flow := oauthLoginFlow()
	refreshTmpl, _ := deriveRefreshTemplate(flow)
	templates := map[domain.ID]domain.APITemplate{refreshTmpl.ID: refreshTmpl}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, templates, load.WithGuard(guardFor(t, srv.URL)))
	rf := NewRefreshTokenFunc(runner, refreshTmpl, 1)

	cur := domain.Credential{Secret: "access-1", Refresh: "refresh-1"}
	if _, err := rf(context.Background(), 0, cur); err == nil {
		t.Fatal("a refresh response with no access token must error")
	}
}

// TestRefreshTokenURLEncoded pins M1: an opaque / standard-base64 refresh token
// containing +, /, = and a space is form-safe in the derived refresh body. The
// {{.refreshToken}} placeholder is rendered through text/template's urlquery, so the
// token endpoint (which decodes via url.ParseQuery) receives the EXACTLY CORRECT
// original refresh token. Without urlquery the raw substitution would corrupt the
// form: a "+" decodes to a space, a bare "=" splits the field, breaking the exchange.
func TestRefreshTokenURLEncoded(t *testing.T) {
	const opaque = "aGVsbG8+d29y/bGQ= and+more" // contains + / = and a space
	var sawRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sawRefresh = r.PostFormValue("refresh_token") // url.ParseQuery-decoded
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "refresh_token": "refresh-2"})
	}))
	defer srv.Close()

	flow := oauthLoginFlow()
	refreshTmpl, ok := deriveRefreshTemplate(flow)
	if !ok {
		t.Fatal("expected the oauth flow to derive a refresh template")
	}
	// Guard against a regression to raw substitution: the derived body must route the
	// refresh token through urlquery, not emit a bare {{.refreshToken}}.
	if !strings.Contains(refreshTmpl.PayloadTemplate, "{{.refreshToken | urlquery}}") {
		t.Fatalf("derived body must encode the refresh token via urlquery; got %q", refreshTmpl.PayloadTemplate)
	}

	templates := map[domain.ID]domain.APITemplate{refreshTmpl.ID: refreshTmpl}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, templates, load.WithGuard(guardFor(t, srv.URL)))
	rf := NewRefreshTokenFunc(runner, refreshTmpl, 1)

	cur := domain.Credential{Subject: "alice", Secret: "access-1", Refresh: opaque}
	got, err := rf(context.Background(), 0, cur)
	if err != nil {
		t.Fatalf("refresh exchange: %v", err)
	}
	if sawRefresh != opaque {
		t.Errorf("token endpoint decoded refresh_token = %q, want %q (the exact original opaque token)", sawRefresh, opaque)
	}
	if got.Secret != "access-2" {
		t.Errorf("rotated access token = %q, want access-2", got.Secret)
	}
}

// TestNewRefreshTokenFuncGuarded pins that the exchange runs through the SAME
// guarded runner: an off-allowlist token host is refused before any traffic.
func TestNewRefreshTokenFuncGuarded(t *testing.T) {
	flow := oauthLoginFlow()
	refreshTmpl, _ := deriveRefreshTemplate(flow)
	templates := map[domain.ID]domain.APITemplate{refreshTmpl.ID: refreshTmpl}
	env := domain.TargetEnv{
		BaseURL:   "http://example.com",
		Allowlist: []string{"127.0.0.1"},
		RateCap:   domain.RateCap{MaxRPS: 10000, MaxConcurrency: 1000},
		EnvClass:  domain.EnvDev,
	}
	g, err := safety.NewGuardForEnv(env, nil, false)
	if err != nil {
		t.Fatalf("guard: %v", err)
	}
	runner := load.NewRunner(load.NewRESTAdapter(time.Second), "http://example.com", templates, load.WithGuard(g))
	rf := NewRefreshTokenFunc(runner, refreshTmpl, 1)
	cur := domain.Credential{Secret: "a", Refresh: "r"}
	_, err = rf(context.Background(), 0, cur)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "allow") {
		t.Fatalf("off-allowlist refresh should be refused by the guard, got %v", err)
	}
}
