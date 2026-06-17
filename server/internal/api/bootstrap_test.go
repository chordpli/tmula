package api

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
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/safety"
)

// bootstrapSignupSUT mints a unique account per signup call and records the bodies.
type bootstrapSignupSUT struct {
	mu     sync.Mutex
	bodies []string
	n      int64
	peak   int64
	cur    int64
}

func newBootstrapSignupSUT() (*httptest.Server, *bootstrapSignupSUT) {
	rec := &bootstrapSignupSUT{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&rec.cur, 1)
		for {
			peak := atomic.LoadInt64(&rec.peak)
			if cur <= peak || atomic.CompareAndSwapInt64(&rec.peak, peak, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond) // widen the concurrency window
		atomic.AddInt64(&rec.cur, -1)

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

// signupPool builds a bootstrap-signup pool with a one-step signup flow capturing
// token and subject, and an optional teardown step.
func signupPool(withTeardown bool) *domain.CredentialPool {
	flow := &domain.SignupFlow{
		Steps: []domain.SignupStep{{
			ID: "register", Method: "POST", Path: "/signup",
			Body:    `{"i":"{{.userIndex}}"}`,
			Extract: map[string]string{"token": "accessToken", "uid": "id"},
		}},
		Start:   "register",
		Capture: domain.SignupCapture{Token: "token", Subject: "uid"},
	}
	if withTeardown {
		flow.Teardown = []domain.SignupStep{{ID: "remove", Method: "DELETE", Path: "/accounts/{{.subject}}"}}
		flow.TeardownStart = "remove"
	}
	return &domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, SignupFlow: flow}
}

// bootstrapSpec wires a bootstrap-signup pool onto the single-node auth-echo spec.
func bootstrapSpec(sutURL string, userCount int, maxConcurrency int) RunSpec {
	spec := specAuth(sutURL, userCount, signupPool(true))
	spec.TargetEnv.RateCap.MaxConcurrency = maxConcurrency
	return spec
}

func newGuardFor(t *testing.T, spec RunSpec) *safety.Guard {
	t.Helper()
	g, err := safety.NewGuardForEnv(spec.TargetEnv, nil, false)
	if err != nil {
		t.Fatalf("guard: %v", err)
	}
	return g
}

// TestBootstrapAuthForProvisionsPerIndex confirms the orchestrator compiles the
// signup flow, builds a bootstrap provider, and Acquire(i) provisions a distinct
// account whose captured token becomes the credential secret.
func TestBootstrapAuthForProvisionsPerIndex(t *testing.T) {
	sut, _ := newBootstrapSignupSUT()
	defer sut.Close()

	spec := bootstrapSpec(sut.URL, 3, 1000)
	boot, err := (&Server{adapter: load.NewRESTAdapter(2 * time.Second)}).bootstrapAuthFor(spec, newGuardFor(t, spec))
	if err != nil {
		t.Fatalf("bootstrapAuthFor: %v", err)
	}
	if boot == nil {
		t.Fatal("bootstrapAuthFor returned nil for a bootstrap pool")
	}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		cred, err := boot.provider.Acquire(context.Background(), i)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if cred.Secret == "" {
			t.Fatalf("acquire %d returned no secret", i)
		}
		if seen[cred.Secret] {
			t.Errorf("acquire %d reused secret %q", i, cred.Secret)
		}
		seen[cred.Secret] = true
	}
}

// TestBootstrapAuthForNonBootstrapIsNil confirms the helper returns (nil,nil) for a
// non-bootstrap pool so callers can branch on it.
func TestBootstrapAuthForNonBootstrapIsNil(t *testing.T) {
	spec := specAuth("http://127.0.0.1:1", 1, twoEntryPool())
	boot, err := (&Server{adapter: load.NewRESTAdapter(time.Second)}).bootstrapAuthFor(spec, newGuardFor(t, spec))
	if err != nil {
		t.Fatalf("bootstrapAuthFor: %v", err)
	}
	if boot != nil {
		t.Fatal("bootstrapAuthFor should return nil for a non-bootstrap pool")
	}
}

// TestBootstrapPrewarmRespectsConcurrencyCap proves the prewarm burst never exceeds
// min(RateCap.MaxConcurrency, bootstrap cap): with MaxConcurrency=2 the signup SUT
// never sees more than 2 in-flight provisions at once, even prewarming many.
func TestBootstrapPrewarmRespectsConcurrencyCap(t *testing.T) {
	sut, rec := newBootstrapSignupSUT()
	defer sut.Close()

	spec := bootstrapSpec(sut.URL, 12, 2)
	boot, err := (&Server{adapter: load.NewRESTAdapter(2 * time.Second)}).bootstrapAuthFor(spec, newGuardFor(t, spec))
	if err != nil {
		t.Fatalf("bootstrapAuthFor: %v", err)
	}
	if err := boot.Prewarm(context.Background(), 12); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	if peak := atomic.LoadInt64(&rec.peak); peak > 2 {
		t.Errorf("prewarm peak concurrency = %d, want <= 2 (the rate cap)", peak)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) != 12 {
		t.Errorf("prewarm provisioned %d accounts, want 12", len(rec.bodies))
	}
}
