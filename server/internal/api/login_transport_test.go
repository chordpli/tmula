package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/runspec"
	"github.com/chordpli/tmula/server/internal/safety"
)

// loginFlowFor builds a one-node login flow that POSTs /login and captures the
// response's access_token into the token variable (and, optionally, a subject).
func loginFlowFor() LoginFlow {
	return LoginFlow{
		Graph:      domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "tlogin"}}},
		Templates:  map[domain.ID]domain.APITemplate{"tlogin": {Method: "POST", Path: "/login", Extract: map[string]string{"token": "access_token", "subject": "user"}}},
		Start:      "login",
		MaxSteps:   4,
		TokenVar:   "token",
		SubjectVar: "subject",
	}
}

// guardFor builds a permissive guard allowing the httptest loopback host so the
// transport's send (which the runner routes through the guard) is admitted.
func guardFor(t *testing.T, baseURL string) *safety.Guard {
	t.Helper()
	env := domain.TargetEnv{
		BaseURL:   baseURL,
		Allowlist: []string{"127.0.0.1"},
		RateCap:   domain.RateCap{MaxRPS: 10000, MaxConcurrency: 1000},
		EnvClass:  domain.EnvDev,
	}
	g, err := safety.NewGuardForEnv(env, nil, false)
	if err != nil {
		t.Fatalf("guard: %v", err)
	}
	return g
}

// TestLoginTokenFuncMintsToken drives the transport end to end: it POSTs the login
// endpoint and returns a credential carrying the captured token (and subject).
func TestLoginTokenFuncMintsToken(t *testing.T) {
	var bodies int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&bodies, 1)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "minted-7", "user": "alice"})
	}))
	defer srv.Close()

	flow := loginFlowFor()
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, flow.Templates, load.WithGuard(guardFor(t, srv.URL)))
	tf, err := NewLoginTokenFunc(runner, flow, 1)
	if err != nil {
		t.Fatalf("new token func: %v", err)
	}

	cred, err := tf(context.Background(), 0)
	if err != nil {
		t.Fatalf("token func: %v", err)
	}
	if cred.Secret != "minted-7" {
		t.Errorf("minted secret = %q, want %q", cred.Secret, "minted-7")
	}
	if cred.Subject != "alice" {
		t.Errorf("minted subject = %q, want %q", cred.Subject, "alice")
	}
	if n := atomic.LoadInt64(&bodies); n != 1 {
		t.Errorf("login endpoint hit %d times for one mint, want 1", n)
	}
}

// TestLoginTokenFuncDistinctPerIndex seeds the login per index so each principal
// is minted independently — the request varies by index (here the index is echoed
// into the body via a template variable the transport threads in).
func TestLoginTokenFuncDistinctPerIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		// Echo the requested index back as the token so the test can assert keying.
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + body["idx"]})
	}))
	defer srv.Close()

	flow := LoginFlow{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
		Templates: map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login", PayloadTemplate: `{"idx":"{{.userIndex}}"}`, Extract: map[string]string{"token": "access_token"}}},
		Start:     "login",
		MaxSteps:  4,
		TokenVar:  "token",
	}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, flow.Templates, load.WithGuard(guardFor(t, srv.URL)))
	tf, err := NewLoginTokenFunc(runner, flow, 1)
	if err != nil {
		t.Fatalf("new token func: %v", err)
	}

	c0, err := tf(context.Background(), 0)
	if err != nil {
		t.Fatalf("mint 0: %v", err)
	}
	c5, err := tf(context.Background(), 5)
	if err != nil {
		t.Fatalf("mint 5: %v", err)
	}
	if c0.Secret != "tok-0" || c5.Secret != "tok-5" {
		t.Errorf("per-index mint = %q,%q; want tok-0,tok-5", c0.Secret, c5.Secret)
	}
}

// TestLoginTokenFuncCredentialPoolRows drives the P8 multi-user login path: the
// login flow carries a credential pool of login-INPUT rows (username/password), and
// the transport seeds {{.username}}/{{.password}} from row i%N before walking the
// flow. VU 0/1/2 each POST a DISTINCT account's credentials and capture a distinct
// token; VU 3 wraps to row 0. The minted credential's Subject defaults to the row's
// username so a finding identifies which user.
func TestLoginTokenFuncCredentialPoolRows(t *testing.T) {
	type loginReq struct {
		U string `json:"u"`
		P string `json:"p"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body loginReq
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		// Echo username+password back so the test can assert each VU logged in as a
		// different account and got an account-specific token.
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + body.U + "-" + body.P})
	}))
	defer srv.Close()

	flow := LoginFlow{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
		Templates: map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login", PayloadTemplate: `{"u":"{{.username}}","p":"{{.password}}"}`, Extract: map[string]string{"token": "access_token"}}},
		Start:     "login",
		MaxSteps:  4,
		TokenVar:  "token",
		Entries: []domain.Credential{
			{Subject: "alice", Secret: "pw-a"},
			{Subject: "bob", Secret: "pw-b"},
			{Subject: "carol", Secret: "pw-c"},
		},
	}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, flow.Templates, load.WithGuard(guardFor(t, srv.URL)))
	tf, err := NewLoginTokenFunc(runner, flow, 1)
	if err != nil {
		t.Fatalf("new token func: %v", err)
	}

	want := []struct {
		secret  string
		subject string
	}{
		{"tok-alice-pw-a", "alice"},
		{"tok-bob-pw-b", "bob"},
		{"tok-carol-pw-c", "carol"},
		{"tok-alice-pw-a", "alice"}, // VU 3 wraps to row 0
	}
	for i, w := range want {
		cred, err := tf(context.Background(), i)
		if err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
		if cred.Secret != w.secret {
			t.Errorf("VU %d secret = %q, want %q (each VU logs in as a different account)", i, cred.Secret, w.secret)
		}
		if cred.Subject != w.subject {
			t.Errorf("VU %d subject = %q, want %q (minted subject defaults to the row's username)", i, cred.Subject, w.subject)
		}
	}
}

// TestLoginTokenFuncRowAliases proves the {{.subject}}/{{.secret}} aliases render
// the same row as {{.username}}/{{.password}} — a login body may use either name.
func TestLoginTokenFuncRowAliases(t *testing.T) {
	var seenU, seenP string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			U string `json:"u"`
			P string `json:"p"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenU, seenP = body.U, body.P
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	}))
	defer srv.Close()

	flow := LoginFlow{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
		Templates: map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login", PayloadTemplate: `{"u":"{{.subject}}","p":"{{.secret}}"}`, Extract: map[string]string{"token": "access_token"}}},
		Start:     "login",
		MaxSteps:  4,
		TokenVar:  "token",
		Entries:   []domain.Credential{{Subject: "alice", Secret: "pw-a"}},
	}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, flow.Templates, load.WithGuard(guardFor(t, srv.URL)))
	tf, err := NewLoginTokenFunc(runner, flow, 1)
	if err != nil {
		t.Fatalf("new token func: %v", err)
	}
	if _, err := tf(context.Background(), 0); err != nil {
		t.Fatalf("mint: %v", err)
	}
	if seenU != "alice" || seenP != "pw-a" {
		t.Errorf("{{.subject}}/{{.secret}} rendered %q/%q, want alice/pw-a", seenU, seenP)
	}
}

// TestLoginTokenFuncRowSubjectOverridable proves an explicit SubjectVar (or an
// auto-detected subject from the response) overrides the row's username as the
// minted credential's Subject — the row default applies only when no subject is
// captured.
func TestLoginTokenFuncRowSubjectOverridable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "uid": "server-id-42"})
	}))
	defer srv.Close()

	flow := LoginFlow{
		Graph:      domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
		Templates:  map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login", PayloadTemplate: `{"u":"{{.username}}"}`, Extract: map[string]string{"token": "access_token", "subject": "uid"}}},
		Start:      "login",
		MaxSteps:   4,
		TokenVar:   "token",
		SubjectVar: "subject",
		Entries:    []domain.Credential{{Subject: "alice", Secret: "pw-a"}},
	}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, flow.Templates, load.WithGuard(guardFor(t, srv.URL)))
	tf, err := NewLoginTokenFunc(runner, flow, 1)
	if err != nil {
		t.Fatalf("new token func: %v", err)
	}
	cred, err := tf(context.Background(), 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if cred.Subject != "server-id-42" {
		t.Errorf("explicit SubjectVar should override the row username, got subject = %q", cred.Subject)
	}
}

// TestLoginTokenFuncNoEntriesUnchanged pins that a login flow with NO entries is
// byte-for-byte the current single-identity login: the userIndex var is still
// threaded, {{.username}}/{{.password}} render empty, and the minted subject comes
// from the flow (or auto-detect), never a row.
func TestLoginTokenFuncNoEntriesUnchanged(t *testing.T) {
	var seenUsername string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenUsername = body["u"]
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + body["idx"], "user": "svc"})
	}))
	defer srv.Close()

	flow := LoginFlow{
		Graph:      domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
		Templates:  map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login", PayloadTemplate: `{"u":"{{.username}}","idx":"{{.userIndex}}"}`, Extract: map[string]string{"token": "access_token", "subject": "user"}}},
		Start:      "login",
		MaxSteps:   4,
		TokenVar:   "token",
		SubjectVar: "subject",
		// No Entries: single-identity login.
	}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, flow.Templates, load.WithGuard(guardFor(t, srv.URL)))
	tf, err := NewLoginTokenFunc(runner, flow, 1)
	if err != nil {
		t.Fatalf("new token func: %v", err)
	}
	cred, err := tf(context.Background(), 3)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if seenUsername != "" {
		t.Errorf("no-entries login rendered {{.username}} = %q, want empty (single-identity, no row)", seenUsername)
	}
	if cred.Secret != "tok-3" {
		t.Errorf("no-entries login should still thread userIndex; secret = %q, want tok-3", cred.Secret)
	}
	if cred.Subject != "svc" {
		t.Errorf("no-entries login subject = %q, want svc (from the flow, not a row)", cred.Subject)
	}
}

// TestLoginTokenFuncErrorsOnEmptyToken fails loudly when the login succeeds but no
// token was captured (a misconfigured capture or an endpoint that returned no
// token), rather than handing back an empty credential that authenticates as
// nobody.
func TestLoginTokenFuncErrorsOnEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"other":"x"}`))
	}))
	defer srv.Close()

	flow := LoginFlow{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
		Templates: map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login", Extract: map[string]string{"token": "missing"}}},
		Start:     "login",
		MaxSteps:  4,
		TokenVar:  "token",
	}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, flow.Templates, load.WithGuard(guardFor(t, srv.URL)))
	tf, _ := NewLoginTokenFunc(runner, flow, 1)
	if _, err := tf(context.Background(), 0); err == nil {
		t.Fatal("token func should error when no token is captured")
	}
}

// TestNewLoginTokenFuncAllowsEmptyTokenVar accepts a flow with no explicit token
// capture: an empty TokenVar means "auto-detect", so construction must succeed (the
// token is resolved from the response at mint time, not rejected up front).
func TestNewLoginTokenFuncAllowsEmptyTokenVar(t *testing.T) {
	flow := loginFlowFor()
	flow.TokenVar = ""
	runner := load.NewRunner(load.NewRESTAdapter(time.Second), "http://127.0.0.1:1", flow.Templates)
	if _, err := NewLoginTokenFunc(runner, flow, 1); err != nil {
		t.Fatalf("empty TokenVar means auto-detect and must build: %v", err)
	}
}

// TestNewLoginTokenFuncRequiresStart still rejects a flow missing a start node —
// the one piece auto-detection cannot supply.
func TestNewLoginTokenFuncRequiresStart(t *testing.T) {
	flow := loginFlowFor()
	flow.Start = ""
	runner := load.NewRunner(load.NewRESTAdapter(time.Second), "http://127.0.0.1:1", flow.Templates)
	if _, err := NewLoginTokenFunc(runner, flow, 1); err == nil {
		t.Fatal("a login flow with no start node should be rejected")
	}
}

// TestLoginTokenFuncGuardRejects proves the transport runs through the safety
// guard: an off-allowlist login host is refused before any traffic is sent.
func TestLoginTokenFuncGuardRejects(t *testing.T) {
	flow := loginFlowFor()
	// Guard allows only 127.0.0.1, but the login target host is example.com.
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
	runner := load.NewRunner(load.NewRESTAdapter(time.Second), "http://example.com", flow.Templates, load.WithGuard(g))
	tf, _ := NewLoginTokenFunc(runner, flow, 1)
	_, err = tf(context.Background(), 0)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "allow") {
		t.Fatalf("off-allowlist login should be refused by the guard, got %v", err)
	}
}

// TestURLQueryLoginFormTemplates pins the rewrite rule: a form-urlencoded login
// template's bare credential-row placeholders ({{.username}}/{{.password}} and
// the {{.subject}}/{{.secret}} aliases) are piped through urlquery so a password
// carrying &, =, + or a space stays form-safe; an already-piped placeholder and a
// non-form (JSON) template are left byte-identical.
func TestURLQueryLoginFormTemplates(t *testing.T) {
	form := domain.APITemplate{
		ID:              "t",
		Method:          "POST",
		Path:            "/oauth/token",
		Headers:         map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		PayloadTemplate: "grant_type=password&username={{.username}}&password={{ .password }}&client_secret={{.password | urlquery}}",
	}
	jsonTmpl := domain.APITemplate{
		ID:              "j",
		Method:          "POST",
		Path:            "/login",
		PayloadTemplate: `{"u":"{{.username}}","p":"{{.password}}"}`,
	}
	got := urlqueryFormLoginTemplates(map[domain.ID]domain.APITemplate{"t": form, "j": jsonTmpl})
	want := "grant_type=password&username={{.username | urlquery}}&password={{.password | urlquery}}&client_secret={{.password | urlquery}}"
	if got["t"].PayloadTemplate != want {
		t.Errorf("form body = %q, want %q", got["t"].PayloadTemplate, want)
	}
	if got["j"].PayloadTemplate != jsonTmpl.PayloadTemplate {
		t.Errorf("JSON body must stay byte-identical, got %q", got["j"].PayloadTemplate)
	}
	// The input map must not be mutated (the spec's templates are shared).
	if form.PayloadTemplate != "grant_type=password&username={{.username}}&password={{ .password }}&client_secret={{.password | urlquery}}" {
		t.Errorf("input template mutated: %q", form.PayloadTemplate)
	}
}

// TestLoginAuthFormBodyEncodesSpecialCharPassword drives the real login path:
// a credential row whose password carries &, =, + and a space must arrive at the
// IdP byte-exact after the form decode — the run-path proof that loginAuthFor
// pipes bare row placeholders through urlquery for form bodies.
func TestLoginAuthFormBodyEncodesSpecialCharPassword(t *testing.T) {
	const rawPassword = "p@ss&word=1+2 3"
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotUser = r.PostFormValue("username")
		gotPass = r.PostFormValue("password")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
	}))
	defer srv.Close()

	flowID := domain.ID("login")
	spec := RunSpec{
		TargetEnv: domain.TargetEnv{BaseURL: srv.URL},
		Seed:      1,
		CredentialPool: &domain.CredentialPool{
			ID:          "p",
			Strategy:    domain.CredLogin,
			LoginFlowID: &flowID,
			Entries:     []domain.Credential{{Subject: "alice", Secret: rawPassword}},
		},
		LoginFlow: &runspec.LoginFlowSpec{
			Graph: domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
			Templates: map[domain.ID]domain.APITemplate{"t": {
				ID:              "t",
				Method:          "POST",
				Path:            "/oauth/token",
				Headers:         map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
				PayloadTemplate: "grant_type=password&username={{.username}}&password={{.password}}",
			}},
			Start:    "login",
			MaxSteps: 2,
		},
	}
	s := NewServer(load.NewRESTAdapter(2 * time.Second))
	la, err := s.loginAuthFor(spec, guardFor(t, srv.URL))
	if err != nil {
		t.Fatalf("loginAuthFor: %v", err)
	}
	cred, err := la.provider.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if cred.Secret != "tok-1" {
		t.Errorf("minted secret = %q, want tok-1", cred.Secret)
	}
	if gotUser != "alice" {
		t.Errorf("IdP saw username %q, want alice", gotUser)
	}
	if gotPass != rawPassword {
		t.Errorf("IdP saw password %q, want %q byte-exact (urlquery must round-trip specials)", gotPass, rawPassword)
	}
}
