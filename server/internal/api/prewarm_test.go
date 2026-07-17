package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// TestPrewarmBounded pins the shared bounded-burst helper: every index acquired
// exactly once, in-flight width never above the bound, first error cancels the
// rest.
func TestPrewarmBounded(t *testing.T) {
	var mu sync.Mutex
	inflight, maxInflight := 0, 0
	seen := map[int]int{}
	err := prewarmBounded(context.Background(), 40, 4, func(_ context.Context, idx int) error {
		mu.Lock()
		inflight++
		if inflight > maxInflight {
			maxInflight = inflight
		}
		seen[idx]++
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("prewarmBounded: %v", err)
	}
	if len(seen) != 40 {
		t.Errorf("acquired %d distinct indices, want 40", len(seen))
	}
	for idx, n := range seen {
		if n != 1 {
			t.Errorf("index %d acquired %d times, want once", idx, n)
		}
	}
	if maxInflight > 4 {
		t.Errorf("max in-flight = %d, want <= 4", maxInflight)
	}
}

// TestPrewarmBoundedFirstErrorAborts: the first failure cancels the burst and
// surfaces.
func TestPrewarmBoundedFirstErrorAborts(t *testing.T) {
	boom := errors.New("idp down")
	var calls int64
	err := prewarmBounded(context.Background(), 100, 2, func(ctx context.Context, idx int) error {
		atomic.AddInt64(&calls, 1)
		if idx == 3 {
			return boom
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the first failure", err)
	}
	if got := atomic.LoadInt64(&calls); got == 100 {
		t.Errorf("all 100 acquires ran despite the failure (no cancellation)")
	}
}

// TestLoginPrewarmParallelBounded drives the real login path: 24 per-user logins
// prewarm through a fake IdP that records its max concurrent in-flight requests —
// the burst must be parallel (fast) yet never wider than the run's rate-cap
// concurrency, so the prewarm cannot load-test the IdP.
func TestLoginPrewarmParallelBounded(t *testing.T) {
	var mu sync.Mutex
	inflight, maxInflight, logins := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inflight++
		if inflight > maxInflight {
			maxInflight = inflight
		}
		logins++
		n := logins
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + strconv.Itoa(n)})
	}))
	defer srv.Close()

	flowID := domain.ID("login")
	spec := RunSpec{
		TargetEnv: domain.TargetEnv{
			BaseURL: srv.URL, Allowlist: []string{"127.0.0.1"},
			RateCap: domain.RateCap{MaxRPS: 10000, MaxConcurrency: 3}, EnvClass: domain.EnvDev,
		},
		Seed: 1,
		CredentialPool: &domain.CredentialPool{
			ID: "p", Strategy: domain.CredLogin, LoginFlowID: &flowID,
		},
		LoginFlow: &runspec.LoginFlowSpec{
			Graph: domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}},
			Templates: map[domain.ID]domain.APITemplate{"t": {
				ID: "t", Method: "POST", Path: "/login",
				PayloadTemplate: `{"u":"u-{{.userIndex}}"}`,
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
	start := time.Now()
	if err := la.Prewarm(context.Background(), 24); err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	elapsed := time.Since(start)

	mu.Lock()
	defer mu.Unlock()
	if logins != 24 {
		t.Errorf("logins = %d, want 24", logins)
	}
	if maxInflight > 3 {
		t.Errorf("max in-flight = %d, want <= RateCap.MaxConcurrency (3) — the prewarm must not load-test the IdP", maxInflight)
	}
	if maxInflight < 2 {
		t.Errorf("max in-flight = %d, want >= 2 (the prewarm must actually parallelize)", maxInflight)
	}
	// 24 sequential 5ms logins would be >= 120ms; the bounded burst should be
	// well under (24/3 * 5ms = 40ms + overhead). Generous bound to avoid flakes.
	if elapsed > 110*time.Millisecond {
		t.Errorf("prewarm took %v, want parallel speedup (sequential would be >= 120ms)", elapsed)
	}
}
