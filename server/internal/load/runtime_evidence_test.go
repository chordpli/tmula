package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestStepResultsCarrySeedAndFailurePath: every step result carries the
// session's walk seed (the reproduce coordinate), and FAILED steps — and only
// failed steps — additionally carry the node path walked up to and including
// the failure, so evidence can show the journey without paying a per-step
// path cost on healthy traffic.
func TestStepResultsCarrySeedAndFailurePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/b") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	const seed = int64(42)
	results, err := r.RunSession(context.Background(), g, "a", 5, VirtualUser{ID: "u1"}, seed, nil)
	if err != nil {
		t.Fatalf("run session: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}

	ok, fail := results[0], results[1]
	if ok.NodeID != "a" || fail.NodeID != "b" {
		t.Fatalf("unexpected step order: %+v", results)
	}
	if ok.Seed != seed || fail.Seed != seed {
		t.Errorf("seeds = %d/%d, want both %d", ok.Seed, fail.Seed, seed)
	}
	if ok.Path != nil {
		t.Errorf("successful step carries a path %v; only failures should", ok.Path)
	}
	if len(fail.Path) != 2 || fail.Path[0] != "a" || fail.Path[1] != "b" {
		t.Errorf("failed step path = %v, want the walk up to the failure [a b]", fail.Path)
	}
}

// TestRunStampsPerUserSeeds: the closed fan-out stamps each user's results
// with that user's own derived seed (base+i), so a failure deep in a big pool
// still names the exact seed that reproduces its walk.
func TestRunStampsPerUserSeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	users := []VirtualUser{{ID: "u0"}, {ID: "u1"}, {ID: "u2"}}
	const base = int64(100)
	results, err := r.Run(context.Background(), g, "a", 5, users, base)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	seedByUser := map[string]int64{}
	for _, sr := range results {
		seedByUser[sr.UserID] = sr.Seed
	}
	want := map[string]int64{"u0": base, "u1": base + 1, "u2": base + 2}
	for id, w := range want {
		if seedByUser[id] != w {
			t.Errorf("seed for %s = %d, want %d", id, seedByUser[id], w)
		}
	}
}

// TestWalkFailureResultCarriesSeed: even the synthetic result emitted when the
// walk itself cannot start carries the seed, so a misconfigured-graph failure
// is still attributable.
func TestWalkFailureResultCarriesSeed(t *testing.T) {
	g := domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a"}}}
	r := NewRunner(NewRESTAdapter(time.Second), "http://127.0.0.1:0", nil)

	results, err := r.RunSession(context.Background(), g, "missing", 5, VirtualUser{ID: "u1"}, 7, nil)
	if err != nil {
		t.Fatalf("run session: %v", err)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("want one error result, got %+v", results)
	}
	if results[0].Seed != 7 {
		t.Errorf("seed = %d, want 7", results[0].Seed)
	}
}
