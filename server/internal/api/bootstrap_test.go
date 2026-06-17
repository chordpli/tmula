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

// teardownSUT records signups and deletes. /signup mints accounts; /accounts/{id}
// DELETE records the torn-down id.
type teardownSUT struct {
	mu       sync.Mutex
	signups  int
	deleted  []string
	failOnID string // a DELETE for this account id 500s (partial-failure test)
}

func newTeardownSUT() (*httptest.Server, *teardownSUT) {
	rec := &teardownSUT{}
	mux := http.NewServeMux()
	mux.HandleFunc("/signup", func(w http.ResponseWriter, _ *http.Request) {
		rec.mu.Lock()
		rec.signups++
		id := "acct-" + strconv.Itoa(rec.signups)
		rec.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "tok-" + id, "id": id})
	})
	mux.HandleFunc("/accounts/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/accounts/"):]
		rec.mu.Lock()
		fail := id == rec.failOnID
		if !fail {
			rec.deleted = append(rec.deleted, id)
		}
		rec.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return httptest.NewServer(mux), rec
}

func (r *teardownSUT) deletedSet() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for _, d := range r.deleted {
		out[d] = true
	}
	return out
}

// TestTeardownDeprovisionsThroughCompiledFlow drives the full orchestrator wiring:
// provision 3 accounts, then runTeardown walks the compiled DELETE journey for each,
// removing exactly the provisioned subjects.
func TestTeardownDeprovisionsThroughCompiledFlow(t *testing.T) {
	sut, rec := newTeardownSUT()
	defer sut.Close()

	spec := bootstrapSpec(sut.URL, 3, 1000)
	srv := &Server{adapter: load.NewRESTAdapter(2 * time.Second)}
	boot, err := srv.bootstrapAuthFor(spec, newGuardFor(t, spec))
	if err != nil {
		t.Fatalf("bootstrapAuthFor: %v", err)
	}
	if err := boot.Prewarm(context.Background(), 3); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	boot.runTeardown(spec.ID(), 3)

	del := rec.deletedSet()
	for i := 1; i <= 3; i++ {
		if !del["acct-"+strconv.Itoa(i)] {
			t.Errorf("account acct-%d was not deprovisioned (deleted=%v)", i, rec.deleted)
		}
	}
}

// TestTeardownFiresOnFreshContextAfterCancel is critic must-have (b): provision under
// a run context, cancel it mid-flight, and assert teardown STILL fires for every
// provisioned account because runTeardown uses a fresh context.Background(), not the
// (now-cancelled) run context.
func TestTeardownFiresOnFreshContextAfterCancel(t *testing.T) {
	sut, rec := newTeardownSUT()
	defer sut.Close()

	spec := bootstrapSpec(sut.URL, 3, 1000)
	srv := &Server{adapter: load.NewRESTAdapter(2 * time.Second)}

	runCtx, cancel := context.WithCancel(context.Background())
	boot, err := srv.bootstrapAuthFor(spec, newGuardFor(t, spec))
	if err != nil {
		t.Fatalf("bootstrapAuthFor: %v", err)
	}
	if err := boot.Prewarm(runCtx, 3); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	// The run is killed mid-flight: cancel the run context BEFORE teardown.
	cancel()

	// Teardown must still deprovision every account — it runs on a fresh context.
	boot.runTeardown(spec.ID(), 3)

	del := rec.deletedSet()
	if len(del) != 3 {
		t.Fatalf("after a cancelled run, teardown deprovisioned %d accounts, want 3 (fresh context)", len(del))
	}
}

// TestTeardownSurvivesPartialFailureThroughOrchestrator is critic must-have (a) at
// the orchestrator level: account 2's DELETE 500s, but the others are still torn
// down and runTeardown does not panic or fail (best-effort).
func TestTeardownSurvivesPartialFailureThroughOrchestrator(t *testing.T) {
	sut, rec := newTeardownSUT()
	rec.failOnID = "acct-2"
	defer sut.Close()

	spec := bootstrapSpec(sut.URL, 3, 1000)
	srv := &Server{adapter: load.NewRESTAdapter(2 * time.Second)}
	boot, err := srv.bootstrapAuthFor(spec, newGuardFor(t, spec))
	if err != nil {
		t.Fatalf("bootstrapAuthFor: %v", err)
	}
	if err := boot.Prewarm(context.Background(), 3); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	boot.runTeardown(spec.ID(), 3) // must not panic or block

	del := rec.deletedSet()
	if !del["acct-1"] || !del["acct-3"] {
		t.Errorf("the surviving accounts were not torn down despite acct-2 failing (deleted=%v)", rec.deleted)
	}
}

// TestKeepAccountsSkipsTeardown proves --keep-accounts builds a provider with no
// teardown func and runTeardown is a no-op: the provisioned accounts are left alive.
func TestKeepAccountsSkipsTeardown(t *testing.T) {
	sut, rec := newTeardownSUT()
	defer sut.Close()

	spec := bootstrapSpec(sut.URL, 2, 1000)
	spec.CredentialPool.KeepAccounts = true
	srv := &Server{adapter: load.NewRESTAdapter(2 * time.Second)}
	boot, err := srv.bootstrapAuthFor(spec, newGuardFor(t, spec))
	if err != nil {
		t.Fatalf("bootstrapAuthFor: %v", err)
	}
	if err := boot.Prewarm(context.Background(), 2); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	boot.runTeardown(spec.ID(), 2)

	if del := rec.deletedSet(); len(del) != 0 {
		t.Errorf("keep-accounts run deprovisioned %d accounts, want 0 (left alive)", len(del))
	}
}
