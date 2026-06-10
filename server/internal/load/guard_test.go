package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/safety"
)

// oneNode builds a single-request graph hitting the given path.
func oneNode(path string) (domain.ScenarioGraph, map[domain.ID]domain.APITemplate) {
	return domain.ScenarioGraph{
			ID:    "g",
			Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}},
		}, map[domain.ID]domain.APITemplate{
			"ta": {Method: "GET", Path: path},
		}
}

func newGuard(t *testing.T, allow ...string) *safety.Guard {
	t.Helper()
	g, err := safety.NewGuard(safety.Config{Allowlist: allow, MaxRPS: 1000, MaxConcurrency: 100})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	return g
}

func runOne(t *testing.T, baseURL, path string, guard *safety.Guard) []StepResult {
	t.Helper()
	g, tmpls := oneNode(path)
	runner := NewRunner(NewRESTAdapter(2*time.Second), baseURL, tmpls, WithGuard(guard))
	res, err := runner.Run(context.Background(), g, "a", 2, []VirtualUser{{ID: "u"}}, 1)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// TestGuardBlocksOffAllowlistHost: a request to a host not on the allowlist
// never reaches the SUT and is recorded as an error.
func TestGuardBlocksOffAllowlistHost(t *testing.T) {
	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	res := runOne(t, sut.URL, "/a", newGuard(t, "example.com")) // SUT host is NOT example.com
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Errorf("SUT hits = %d, want 0 (request should be blocked off-allowlist)", got)
	}
	if len(res) == 0 || res[0].Err == nil {
		t.Errorf("blocked request should be recorded with an error, got %+v", res)
	}
}

// TestGuardBlocksHostOverridingPath: even when the base host IS allowlisted, a
// template path that overrides the authority (e.g. "@evil.com/x") is blocked,
// because the allowlist is checked against the rendered URL.
func TestGuardBlocksHostOverridingPath(t *testing.T) {
	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	// base host (127.0.0.1) is allowlisted, but the path redirects to evil.com.
	res := runOne(t, sut.URL, "@evil.com/x", newGuard(t, "127.0.0.1"))
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Errorf("SUT hits = %d, want 0 (host-override path must be blocked)", got)
	}
	if len(res) == 0 || res[0].Err == nil {
		t.Errorf("host-override request should be recorded with an error, got %+v", res)
	}
}

// TestGuardAllowsAllowlistedHost: a normal allowlisted request goes through.
func TestGuardAllowsAllowlistedHost(t *testing.T) {
	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	res := runOne(t, sut.URL, "/a", newGuard(t, "127.0.0.1"))
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("SUT hits = %d, want 1 (allowlisted request should pass)", got)
	}
	if len(res) == 0 || res[0].Err != nil {
		t.Errorf("allowlisted request should succeed, got %+v", res)
	}
}
