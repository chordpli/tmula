package demo

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// get/post issue one request against the shop handler and return the status.
func do(t *testing.T, h http.Handler, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

// TestShopHappyEndpointsHealthy: the endpoints without planted bugs must never
// fail — they are the healthy baseline the findings stand out against.
func TestShopHappyEndpointsHealthy(t *testing.T) {
	h := NewShop().Handler()
	for _, path := range []string{"/browse", "/category", "/healthz"} {
		for i := 0; i < 10; i++ {
			if code := do(t, h, http.MethodGet, path); code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200 (healthy endpoint must not flake)", path, code)
			}
		}
	}
}

// TestShopCartPlantedBug: the cart carries the demo's headline planted bug — an
// intermittent ~8%% 500. Over 200 requests at p=0.08 the chance of seeing zero
// failures is 0.92^200 ≈ 6e-8, so requiring at least one (while the majority
// still succeeds) is a stable assertion, not a flaky one.
func TestShopCartPlantedBug(t *testing.T) {
	h := NewShop().Handler()
	var ok, failed int
	for i := 0; i < 200; i++ {
		switch code := do(t, h, http.MethodPost, "/cart"); code {
		case http.StatusOK:
			ok++
		case http.StatusInternalServerError:
			failed++
		default:
			t.Fatalf("POST /cart = %d, want 200 or 500", code)
		}
	}
	if failed == 0 {
		t.Error("POST /cart never returned 500 in 200 requests; the planted cart bug is gone")
	}
	if ok <= failed {
		t.Errorf("cart ok=%d failed=%d; the bug must stay intermittent, not dominant", ok, failed)
	}
}

// TestShopCheckoutDegradesButAnswers: checkout fails a load-dependent fraction
// of requests with 503 but must never be fully down. Concurrent requests raise
// the in-flight count, so the planted degradation is exercised on the same path
// the simulator drives it on.
func TestShopCheckoutDegradesButAnswers(t *testing.T) {
	srv := httptest.NewServer(NewShop().Handler())
	defer srv.Close()

	const workers, perWorker = 20, 10
	var mu sync.Mutex
	counts := map[int]int{}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				resp, err := http.Post(srv.URL+"/checkout", "application/json", nil)
				if err != nil {
					t.Errorf("POST /checkout: %v", err)
					return
				}
				resp.Body.Close()
				mu.Lock()
				counts[resp.StatusCode]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Base failure probability is 8% climbing with concurrency: zero 503s in
	// 200 concurrent checkouts is (<0.92)^200 ≈ 6e-8 — effectively impossible.
	if counts[http.StatusServiceUnavailable] == 0 {
		t.Errorf("checkout never returned 503 under concurrency (counts=%v); the planted degradation is gone", counts)
	}
	// The failure probability is capped at 40%, so success must stay the norm.
	if counts[http.StatusOK] <= counts[http.StatusServiceUnavailable] {
		t.Errorf("checkout counts=%v; degraded must not read as fully down", counts)
	}
}

// TestShopInstancesDoNotShareState: each Shop tracks its own checkout
// concurrency, so two demo runs in one process (e.g. parallel tests) cannot
// skew each other's failure probability.
func TestShopInstancesDoNotShareState(t *testing.T) {
	a, b := NewShop(), NewShop()
	a.checkoutInflight.Add(5)
	if got := b.checkoutInflight.Load(); got != 0 {
		t.Errorf("second shop inherited inflight=%d, want 0 (state must be per instance)", got)
	}
}
