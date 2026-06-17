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
