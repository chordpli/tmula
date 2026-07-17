package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// loginFlowNoCapture is a login flow with NO explicit token capture (empty
// TokenVar, no extract) — it relies on auto-detection from the response.
func loginFlowNoCapture() LoginFlow {
	return LoginFlow{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "tlogin"}}},
		Templates: map[domain.ID]domain.APITemplate{"tlogin": {Method: "POST", Path: "/login"}},
		Start:     "login",
		// TokenVar deliberately empty: auto-detect.
	}
}

// TestLoginAutoDetectsToken mints a token from a login flow that declares no
// explicit capture: the transport falls back to DetectCredential on the response.
func TestLoginAutoDetectsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "auto-tok", "username": "alice"})
	}))
	defer srv.Close()

	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, loginFlowNoCapture().Templates)
	tokenFunc, err := NewLoginTokenFunc(runner, loginFlowNoCapture(), 1)
	if err != nil {
		t.Fatalf("build token func: %v", err)
	}
	cred, err := tokenFunc(context.Background(), 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if cred.Secret != "auto-tok" {
		t.Errorf("secret = %q, want %q", cred.Secret, "auto-tok")
	}
	if cred.Subject != "alice" {
		t.Errorf("subject = %q, want %q (auto-detected)", cred.Subject, "alice")
	}
}

// TestLoginAutoDetectsCookieToken mints a session-cookie token when the login
// response carries no body token and no explicit capture.
func TestLoginAutoDetectsCookieToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "sess-tok", Path: "/"})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, loginFlowNoCapture().Templates)
	tokenFunc, err := NewLoginTokenFunc(runner, loginFlowNoCapture(), 1)
	if err != nil {
		t.Fatalf("build token func: %v", err)
	}
	cred, err := tokenFunc(context.Background(), 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if cred.Secret != "sess-tok" {
		t.Errorf("secret = %q, want the session cookie (%q)", cred.Secret, "sess-tok")
	}
}

// TestLoginAutoDetectFailsWhenNothingFound errors clearly when a flow declares no
// explicit capture AND the response carries nothing detectable — the run must fail
// rather than authenticate as nobody.
func TestLoginAutoDetectFailsWhenNothingFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"logged in","count":1}`))
	}))
	defer srv.Close()

	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, loginFlowNoCapture().Templates)
	tokenFunc, err := NewLoginTokenFunc(runner, loginFlowNoCapture(), 1)
	if err != nil {
		t.Fatalf("build token func: %v", err)
	}
	if _, err := tokenFunc(context.Background(), 0); err == nil {
		t.Fatal("expected an error when no token can be auto-detected")
	}
}

// TestLoginExplicitCaptureWinsOverAutoDetect proves an explicit TokenVar still
// takes the captured variable even when the response also carries an auto-
// detectable field — explicit capture is authoritative.
func TestLoginExplicitCaptureWinsOverAutoDetect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// "custom" is what the explicit extract names; "access_token" is what auto-
		// detect would pick. They differ so the test can tell which path ran.
		_ = json.NewEncoder(w).Encode(map[string]string{"custom": "explicit-tok", "access_token": "auto-tok"})
	}))
	defer srv.Close()

	flow := LoginFlow{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "tlogin"}}},
		Templates: map[domain.ID]domain.APITemplate{"tlogin": {Method: "POST", Path: "/login", Extract: map[string]string{"tok": "custom"}}},
		Start:     "login",
		TokenVar:  "tok",
	}
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, flow.Templates)
	tokenFunc, err := NewLoginTokenFunc(runner, flow, 1)
	if err != nil {
		t.Fatalf("build token func: %v", err)
	}
	cred, err := tokenFunc(context.Background(), 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if cred.Secret != "explicit-tok" {
		t.Errorf("secret = %q, want the explicitly captured token (%q)", cred.Secret, "explicit-tok")
	}
}
