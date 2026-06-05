package bench

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// twoNodeGraph is a linear a->b graph; each user issues exactly one request per
// node, so a clean run issues users*2 requests.
func twoNodeGraph() (domain.ScenarioGraph, map[domain.ID]domain.APITemplate) {
	g := domain.ScenarioGraph{
		ID:    "cap",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "tb"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1.0}},
	}
	tmpls := map[domain.ID]domain.APITemplate{
		"ta": {Protocol: domain.ProtocolREST, Method: "GET", Path: "/a"},
		"tb": {Protocol: domain.ProtocolREST, Method: "GET", Path: "/b"},
	}
	return g, tmpls
}

// okServer returns 200 with an optional tiny sleep so latency percentiles are
// populated with non-trivial values.
func okServer(sleep time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sleep > 0 {
			time.Sleep(sleep)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func TestRunMeasuresCapacity(t *testing.T) {
	srv := okServer(time.Millisecond)
	defer srv.Close()

	g, tmpls := twoNodeGraph()
	const (
		users = 100
		nodes = 2
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := Run(ctx, Options{
		BaseURL:   srv.URL,
		Graph:     g,
		Templates: tmpls,
		Start:     "a",
		Users:     users,
		MaxSteps:  5,
		Timeout:   2 * time.Second,
		Seed:      1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.TargetConcurrency != users {
		t.Errorf("TargetConcurrency = %d, want %d", res.TargetConcurrency, users)
	}
	if res.TotalRequests != users*nodes {
		t.Errorf("TotalRequests = %d, want %d", res.TotalRequests, users*nodes)
	}
	if res.ErrorRate != 0 {
		t.Errorf("ErrorRate = %v, want 0", res.ErrorRate)
	}
	if res.AchievedRPS <= 0 {
		t.Errorf("AchievedRPS = %v, want > 0", res.AchievedRPS)
	}
	if res.DurationMs <= 0 {
		t.Errorf("DurationMs = %v, want > 0", res.DurationMs)
	}
	// A clean run against a deterministic SUT issues exactly the expected count.
	if res.TrackingErrorPct != 0 {
		t.Errorf("TrackingErrorPct = %v, want 0", res.TrackingErrorPct)
	}
	// Percentiles must be populated (the SUT sleeps ~1ms, so latency > 0).
	if res.P50 <= 0 || res.P95 <= 0 || res.P99 <= 0 {
		t.Errorf("latency percentiles not populated: p50=%v p95=%v p99=%v", res.P50, res.P95, res.P99)
	}
	if res.P99 < res.P50 {
		t.Errorf("p99 (%v) should be >= p50 (%v)", res.P99, res.P50)
	}
}

func TestRunValidatesOptions(t *testing.T) {
	g, tmpls := twoNodeGraph()
	base := Options{Graph: g, Templates: tmpls, Start: "a", MaxSteps: 5, Timeout: time.Second, Users: 1}

	t.Run("zero users", func(t *testing.T) {
		opts := base
		opts.Users = 0
		if _, err := Run(context.Background(), opts); err == nil {
			t.Fatal("expected error for Users <= 0")
		}
	})
	t.Run("zero timeout", func(t *testing.T) {
		opts := base
		opts.Timeout = 0
		if _, err := Run(context.Background(), opts); err == nil {
			t.Fatal("expected error for Timeout <= 0")
		}
	})
}

func TestRunSetupErrorPropagates(t *testing.T) {
	// A node referencing a template that is not supplied is a setup failure the
	// runner surfaces; Run must wrap and return it.
	g := domain.ScenarioGraph{Nodes: []domain.Node{{ID: "a", APITemplateID: "missing"}}}
	_, err := Run(context.Background(), Options{
		BaseURL:   "http://127.0.0.1:0",
		Graph:     g,
		Templates: map[domain.ID]domain.APITemplate{},
		Start:     "a",
		Users:     1,
		MaxSteps:  1,
		Timeout:   time.Second,
	})
	if err == nil {
		t.Fatal("expected setup error for unknown template")
	}
}

func TestTrackingErrorPct(t *testing.T) {
	cases := []struct {
		name     string
		achieved int
		expected int
		want     float64
	}{
		{"perfect tracking", 200, 200, 0},
		{"under by 10pct", 180, 200, 10},
		{"over by 5pct", 210, 200, 5},
		{"half dropped", 100, 200, 50},
		{"nothing expected nothing done", 0, 0, 0},
		{"unexpected with no baseline", 7, 0, 100},
		{"single request perfect", 1, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TrackingErrorPct(tc.achieved, tc.expected)
			if got != tc.want {
				t.Errorf("TrackingErrorPct(%d, %d) = %v, want %v", tc.achieved, tc.expected, got, tc.want)
			}
		})
	}
}

// BenchmarkLocalCapacity drives a moderate concurrency against an httptest SUT
// to measure local capacity. It reports the achieved RPS as a custom metric so
// `go test -bench` surfaces the realized throughput. Run a single iteration with
// `-benchtime=1x` for a fast smoke check; raise it to load-test locally.
func BenchmarkLocalCapacity(b *testing.B) {
	srv := okServer(0)
	defer srv.Close()

	g, tmpls := twoNodeGraph()

	b.ResetTimer()
	var lastRPS float64
	for i := 0; i < b.N; i++ {
		res, err := Run(context.Background(), Options{
			BaseURL:   srv.URL,
			Graph:     g,
			Templates: tmpls,
			Start:     "a",
			Users:     200,
			MaxSteps:  5,
			Timeout:   5 * time.Second,
			Seed:      int64(i),
		})
		if err != nil {
			b.Fatalf("Run: %v", err)
		}
		if res.TotalRequests != 200*2 {
			b.Fatalf("TotalRequests = %d, want %d", res.TotalRequests, 200*2)
		}
		lastRPS = res.AchievedRPS
	}
	b.ReportMetric(lastRPS, "achieved-rps")
}
