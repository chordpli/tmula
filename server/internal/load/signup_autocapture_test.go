package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// signupFlowNoCapture is a one-step signup flow with NO explicit capture (empty
// TokenVar, no extract) — it relies on auto-detection of the signup response.
func signupFlowNoCapture() SignupFlow {
	return SignupFlow{
		Graph:     domain.ScenarioGraph{ID: "signup", Nodes: []domain.Node{{ID: "register", APITemplateID: "t_register"}}},
		Templates: map[domain.ID]domain.APITemplate{"t_register": {Method: "POST", Path: "/signup"}},
		Start:     "register",
		// TokenVar deliberately empty: auto-detect.
	}
}

// TestSignupAutoDetectsToken provisions an account from a signup flow that declares
// no explicit capture: the runner falls back to DetectCredential on the response.
func TestSignupAutoDetectsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"access_token":"auto-signup-tok","id":"acct-1"}`))
	}))
	defer srv.Close()

	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, signupFlowNoCapture().Templates)
	signup, err := NewSignupRunner(r, signupFlowNoCapture(), 1)
	if err != nil {
		t.Fatalf("build signup runner: %v", err)
	}
	cred, err := signup(context.Background(), 0)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if cred.Secret != "auto-signup-tok" {
		t.Errorf("secret = %q, want %q", cred.Secret, "auto-signup-tok")
	}
	if cred.Subject != "acct-1" {
		t.Errorf("subject = %q, want auto-detected %q", cred.Subject, "acct-1")
	}
}

// TestSignupAutoDetectFailsWhenNothingFound errors when a signup flow declares no
// explicit capture and the response carries nothing detectable.
func TestSignupAutoDetectFailsWhenNothingFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":"created","count":1}`))
	}))
	defer srv.Close()

	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, signupFlowNoCapture().Templates)
	signup, err := NewSignupRunner(r, signupFlowNoCapture(), 1)
	if err != nil {
		t.Fatalf("build signup runner: %v", err)
	}
	if _, err := signup(context.Background(), 0); err == nil {
		t.Fatal("expected an error when no token can be auto-detected from the signup response")
	}
}

// TestSignupExplicitCaptureWinsOverAutoDetect proves an explicit TokenVar takes the
// captured variable even when the response also carries an auto-detectable field.
func TestSignupExplicitCaptureWinsOverAutoDetect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"custom":"explicit-tok","access_token":"auto-tok"}`))
	}))
	defer srv.Close()

	flow := SignupFlow{
		Graph:     domain.ScenarioGraph{ID: "signup", Nodes: []domain.Node{{ID: "register", APITemplateID: "t_register"}}},
		Templates: map[domain.ID]domain.APITemplate{"t_register": {Method: "POST", Path: "/signup", Extract: map[string]string{"tok": "custom"}}},
		Start:     "register",
		TokenVar:  "tok",
	}
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, flow.Templates)
	signup, err := NewSignupRunner(r, flow, 1)
	if err != nil {
		t.Fatalf("build signup runner: %v", err)
	}
	cred, err := signup(context.Background(), 0)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if cred.Secret != "explicit-tok" {
		t.Errorf("secret = %q, want the explicitly captured token (%q)", cred.Secret, "explicit-tok")
	}
}
