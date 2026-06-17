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

// flakySignupSUT 500s (or 429s) the first failFirst signup calls, then succeeds —
// to exercise bounded backoff. A 409 path is toggled separately.
type flakySignupSUT struct {
	mu        sync.Mutex
	failFirst int
	failCode  int
	calls     int
	conflict  bool // when true, always answer 409 with a token in the body
	noToken   bool // when true, the 409 body carries no token
}

func newFlakySignupSUT(failFirst, failCode int) (*httptest.Server, *flakySignupSUT) {
	rec := &flakySignupSUT{failFirst: failFirst, failCode: failCode}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rec.mu.Lock()
		rec.calls++
		n := rec.calls
		conflict := rec.conflict
		noToken := rec.noToken
		fail := n <= rec.failFirst
		rec.mu.Unlock()

		switch {
		case conflict:
			w.WriteHeader(http.StatusConflict)
			if noToken {
				_, _ = w.Write([]byte(`{"error":"exists"}`))
			} else {
				_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "tok-existing", "id": "acct-existing"})
			}
		case fail:
			w.WriteHeader(rec.failCode)
		default:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "tok-ok", "id": "acct-ok"})
		}
	}))
	return srv, rec
}

// fakeClock records the durations a backoff slept, so a test can assert bounded,
// growing backoff without real waiting.
type fakeClock struct {
	mu     sync.Mutex
	slept  []time.Duration
	cancel bool
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) bool {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	cancel := c.cancel
	c.mu.Unlock()
	if cancel {
		return false // simulate a cancelled context during backoff
	}
	return true
}

func (c *fakeClock) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.slept)
}

// TestSignupRetriesOn5xxWithInjectedClock proves a signup that 500s twice then
// succeeds is retried with bounded backoff measured on the injected clock (no real
// sleeping), and ultimately returns the minted credential.
func TestSignupRetriesOn5xxWithInjectedClock(t *testing.T) {
	sut, _ := newFlakySignupSUT(2, http.StatusInternalServerError)
	defer sut.Close()

	clk := &fakeClock{}
	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1, WithSignupRetry(SignupRetry{MaxAttempts: 5, BaseDelay: 10 * time.Millisecond, Sleep: clk.sleep}))
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	cred, err := signup(context.Background(), 0)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if cred.Secret != "tok-ok" {
		t.Errorf("secret = %q, want tok-ok after retries", cred.Secret)
	}
	if clk.count() != 2 {
		t.Errorf("backoff slept %d times, want 2 (one per 5xx before success)", clk.count())
	}
}

// TestSignupRetriesOn429 confirms 429 (rate limited) is treated as retryable just
// like a 5xx.
func TestSignupRetriesOn429(t *testing.T) {
	sut, _ := newFlakySignupSUT(1, http.StatusTooManyRequests)
	defer sut.Close()

	clk := &fakeClock{}
	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1, WithSignupRetry(SignupRetry{MaxAttempts: 4, BaseDelay: time.Millisecond, Sleep: clk.sleep}))
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	if _, err := signup(context.Background(), 0); err != nil {
		t.Fatalf("signup: %v", err)
	}
	if clk.count() != 1 {
		t.Errorf("backoff slept %d times, want 1 (one 429 before success)", clk.count())
	}
}

// TestSignupBackoffIsBounded proves a persistently failing signup gives up after
// MaxAttempts (not forever) and returns an error.
func TestSignupBackoffIsBounded(t *testing.T) {
	sut, _ := newFlakySignupSUT(100, http.StatusBadGateway)
	defer sut.Close()

	clk := &fakeClock{}
	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1, WithSignupRetry(SignupRetry{MaxAttempts: 3, BaseDelay: time.Millisecond, Sleep: clk.sleep}))
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	if _, err := signup(context.Background(), 0); err == nil {
		t.Fatal("a persistently failing signup should error after MaxAttempts")
	}
	// 3 attempts => 2 backoffs between them.
	if clk.count() != 2 {
		t.Errorf("backoff slept %d times, want 2 for 3 bounded attempts", clk.count())
	}
}

// TestSignupBackoffIsCancellable proves a cancelled context during backoff stops the
// retry loop promptly with an error rather than continuing to retry.
func TestSignupBackoffIsCancellable(t *testing.T) {
	sut, _ := newFlakySignupSUT(100, http.StatusInternalServerError)
	defer sut.Close()

	clk := &fakeClock{cancel: true} // the first backoff "cancels"
	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1, WithSignupRetry(SignupRetry{MaxAttempts: 10, BaseDelay: time.Millisecond, Sleep: clk.sleep}))
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	if _, err := signup(context.Background(), 0); err == nil {
		t.Fatal("a cancelled backoff should return an error")
	}
	if clk.count() != 1 {
		t.Errorf("cancelled backoff slept %d times, want 1 (it stops promptly)", clk.count())
	}
}

// TestSignup409IsSuccess is the idempotency contract: a deterministic 409 (account
// already exists) is treated as success and the token is captured from the 409
// response body.
func TestSignup409IsSuccess(t *testing.T) {
	sut, rec := newFlakySignupSUT(0, 0)
	rec.conflict = true
	defer sut.Close()

	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1)
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	cred, err := signup(context.Background(), 0)
	if err != nil {
		t.Fatalf("409 signup should succeed: %v", err)
	}
	if cred.Secret != "tok-existing" {
		t.Errorf("secret = %q, want tok-existing (captured from 409)", cred.Secret)
	}
}

// TestSignup409WithoutTokenErrors proves a 409 that carries no token (and no
// recover step in the flow) is a clear error, not a silent empty credential.
func TestSignup409WithoutTokenErrors(t *testing.T) {
	sut, rec := newFlakySignupSUT(0, 0)
	rec.conflict = true
	rec.noToken = true
	defer sut.Close()

	r := NewRunner(NewRESTAdapter(2*time.Second), sut.URL, signupFlowFixture().Templates)
	signup, err := NewSignupRunner(r, signupFlowFixture(), 1)
	if err != nil {
		t.Fatalf("new signup runner: %v", err)
	}
	if _, err := signup(context.Background(), 0); err == nil {
		t.Fatal("a 409 with no recoverable token should error clearly")
	}
}
