package load

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// signupSUT records each signup request body and mints a unique token per call so
// a test can assert each index provisioned a distinct account.
type signupSUT struct {
	mu     sync.Mutex
	bodies []string
	n      int64
}

func newSignupSUT() (*httptest.Server, *signupSUT) {
	rec := &signupSUT{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(b))
		rec.mu.Unlock()
		n := atomic.AddInt64(&rec.n, 1)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"accessToken": "tok-" + strconv.FormatInt(n, 10),
			"id":          "acct-" + strconv.FormatInt(n, 10),
		})
	}))
	return srv, rec
}

// signupFlowFixture is a one-step compiled signup flow that captures the token and
// subject from the response.
func signupFlowFixture() SignupFlow {
	return SignupFlow{
		Graph: domain.ScenarioGraph{
			ID:    "signup",
			Nodes: []domain.Node{{ID: "register", APITemplateID: "t_register"}},
		},
		Templates: map[domain.ID]domain.APITemplate{
			"t_register": {
				Method:          "POST",
				Path:            "/signup",
				PayloadTemplate: `{"i":"{{.userIndex}}"}`,
				Extract:         map[string]string{"token": "accessToken", "uid": "id"},
			},
		},
		Start:      "register",
		TokenVar:   "token",
		SubjectVar: "uid",
	}
}

// TestNewSignupRunnerProvisionsCredential walks the signup flow once and confirms
// the captured token becomes the credential's secret and the captured subject its
// (non-sensitive) subject.
func TestNewSignupRunnerProvisionsCredential(t *testing.T) {
	sut, _ := newSignupSUT()
	defer sut.Close()

	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1)
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	cred, err := signup(context.Background(), 0)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if cred.Secret != "tok-1" {
		t.Errorf("secret = %q, want tok-1", cred.Secret)
	}
	if cred.Subject != "acct-1" {
		t.Errorf("subject = %q, want acct-1", cred.Subject)
	}
}

// TestNewSignupRunnerWalksOncePerIndex confirms each index walks the flow once and
// each render sees its own {{.userIndex}}.
func TestNewSignupRunnerWalksOncePerIndex(t *testing.T) {
	sut, rec := newSignupSUT()
	defer sut.Close()

	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1)
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := signup(context.Background(), i); err != nil {
			t.Fatalf("signup %d: %v", i, err)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) != 3 {
		t.Fatalf("signup requests = %d, want 3 (one walk per index)", len(rec.bodies))
	}
	for i, b := range rec.bodies {
		want := `{"i":"` + strconv.Itoa(i) + `"}`
		if b != want {
			t.Errorf("signup body[%d] = %q, want %q", i, b, want)
		}
	}
}

// TestNewSignupRunnerIsFindingsIsolated proves the signup walk never touches the
// runner's result/event sinks (the load-bearing findings-isolation invariant,
// reused from the login path, not re-derived).
func TestNewSignupRunnerIsFindingsIsolated(t *testing.T) {
	sut, _ := newSignupSUT()
	defer sut.Close()

	var sinkCalls, eventCalls int64
	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates,
		WithResultSink(func(StepResult) { atomic.AddInt64(&sinkCalls, 1) }),
		WithEventSink(func(StepEvent) { atomic.AddInt64(&eventCalls, 1) }),
	)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1)
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	if _, err := signup(context.Background(), 0); err != nil {
		t.Fatalf("signup: %v", err)
	}
	if n := atomic.LoadInt64(&sinkCalls); n != 0 {
		t.Errorf("signup hit the result sink %d times, want 0 (findings isolation)", n)
	}
	if n := atomic.LoadInt64(&eventCalls); n != 0 {
		t.Errorf("signup hit the event sink %d times, want 0 (findings isolation)", n)
	}
}

// TestNewSignupRunnerErrorsWithoutToken makes a signup that captured no token a
// loud error, so the caller fails rather than authenticating as nobody.
func TestNewSignupRunnerErrorsWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"acct-1"}`)) // no accessToken
	}))
	defer srv.Close()

	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1)
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	if _, err := signup(context.Background(), 0); err == nil {
		t.Fatal("signup that captured no token should error")
	}
}

// TestNewSignupRunnerRequiresTokenVar rejects a flow with no token capture at
// construction time.
func TestNewSignupRunnerRequiresTokenVar(t *testing.T) {
	flow := signupFlowFixture()
	flow.TokenVar = ""
	r := NewRunner(NewRESTAdapter(2*time.Second), "http://127.0.0.1:1", flow.Templates)
	if _, err := NewSignupRunner(r, flow, 1); err == nil {
		t.Fatal("a signup flow with no token capture variable should be rejected")
	}
}
