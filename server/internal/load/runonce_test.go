package load

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestRunOnceCapturesVariables walks a one-node login flow once and confirms the
// captured response field is returned as an extracted variable.
func TestRunOnceCapturesVariables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "minted-xyz"})
	}))
	defer srv.Close()

	g := domain.ScenarioGraph{
		ID:    "login",
		Nodes: []domain.Node{{ID: "login", APITemplateID: "tlogin"}},
	}
	tmpls := map[domain.ID]domain.APITemplate{
		"tlogin": {
			Method:          "POST",
			Path:            "/login",
			PayloadTemplate: `{"u":"alice"}`,
			Extract:         map[string]string{"token": "access_token"},
		},
	}
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	nodeTmpl, err := r.ResolveNodeTemplates(g)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	vars, err := r.RunOnce(context.Background(), g, nodeTmpl, "login", 4, VirtualUser{ID: "login-0"}, 1)
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if vars["token"] != "minted-xyz" {
		t.Errorf("captured token = %q, want %q", vars["token"], "minted-xyz")
	}
}

// TestRunOnceIsFindingsIsolated proves the helper never touches the runner's
// result/event sinks: even with both wired, walking a login flow produces zero
// observations and zero events. This is the load-bearing findings-isolation
// invariant the login/refresh path relies on.
func TestRunOnceIsFindingsIsolated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tok":"v"}`))
	}))
	defer srv.Close()

	var sinkCalls, eventCalls int64
	g := domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}}
	tmpls := map[domain.ID]domain.APITemplate{
		"t": {Method: "POST", Path: "/login", Extract: map[string]string{"token": "tok"}},
	}
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls,
		WithResultSink(func(StepResult) { atomic.AddInt64(&sinkCalls, 1) }),
		WithEventSink(func(StepEvent) { atomic.AddInt64(&eventCalls, 1) }),
	)

	nodeTmpl, err := r.ResolveNodeTemplates(g)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := r.RunOnce(context.Background(), g, nodeTmpl, "login", 4, VirtualUser{ID: "login-0"}, 1); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if n := atomic.LoadInt64(&sinkCalls); n != 0 {
		t.Errorf("runOnce hit the result sink %d times, want 0 (findings isolation)", n)
	}
	if n := atomic.LoadInt64(&eventCalls); n != 0 {
		t.Errorf("runOnce hit the event sink %d times, want 0 (findings isolation)", n)
	}
}

// TestRunOnceErrorsOnNon2xx surfaces a failed login (a non-2xx status on a flow
// step) as an error rather than silently returning empty variables, so the caller
// (the login transport) can fail the run instead of authenticating as nobody.
func TestRunOnceErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	g := domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}}
	tmpls := map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login"}}
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	nodeTmpl, err := r.ResolveNodeTemplates(g)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := r.RunOnce(context.Background(), g, nodeTmpl, "login", 4, VirtualUser{ID: "login-0"}, 1); err == nil {
		t.Fatal("runOnce should error on a non-2xx login status")
	}
}

// TestRunOnceCarriesCredentialVars proves the seed credential and per-user vars
// render into the login request, so a login flow can template the principal it is
// minting for (e.g. {{.subject}} in the body).
func TestRunOnceCarriesCredentialVars(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b := make([]byte, req.ContentLength)
		_, _ = req.Body.Read(b)
		gotBody.Store(string(b))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tok":"v"}`))
	}))
	defer srv.Close()

	g := domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}}
	tmpls := map[domain.ID]domain.APITemplate{
		"t": {Method: "POST", Path: "/login", PayloadTemplate: `{"who":"{{.subject}}"}`, Extract: map[string]string{"token": "tok"}},
	}
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	nodeTmpl, err := r.ResolveNodeTemplates(g)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	user := VirtualUser{ID: "login-3", Cred: domain.Credential{Subject: "bob"}}
	if _, err := r.RunOnce(context.Background(), g, nodeTmpl, "login", 4, user, 1); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if got, _ := gotBody.Load().(string); got != `{"who":"bob"}` {
		t.Errorf("login body = %q, want the subject rendered", got)
	}
}
