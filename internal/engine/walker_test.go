package engine

import (
	"testing"

	"github.com/chordpli/tmula/internal/domain"
)

func indexOf(path []domain.ID, id domain.ID) int {
	for i, p := range path {
		if p == id {
			return i
		}
	}
	return -1
}

// TestWalkNeverSkipsDependency: pay depends on cart, so across many runs pay
// must never appear before cart (and never without it).
func TestWalkNeverSkipsDependency(t *testing.T) {
	g := domain.ScenarioGraph{
		ID:    "checkout",
		Nodes: []domain.Node{{ID: "browse"}, {ID: "cart"}, {ID: "pay"}},
		Edges: []domain.Edge{
			{From: "browse", To: "cart", Weight: 0.5, Dependency: false},
			{From: "browse", To: "pay", Weight: 0.5, Dependency: false},
			{From: "cart", To: "pay", Weight: 1.0, Dependency: true},
		},
	}
	w, err := NewWalker(g, 1)
	if err != nil {
		t.Fatalf("new walker: %v", err)
	}
	for i := 0; i < 500; i++ {
		path, err := w.Walk("browse", 5)
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		pCart, pPay := indexOf(path, "cart"), indexOf(path, "pay")
		if pPay >= 0 {
			if pCart < 0 {
				t.Fatalf("run %d: pay reached without cart: %v", i, path)
			}
			if pCart > pPay {
				t.Fatalf("run %d: pay before its dependency cart: %v", i, path)
			}
		}
	}
}

// TestTransitionDistribution: a 0.7/0.3 split should be reflected statistically.
func TestTransitionDistribution(t *testing.T) {
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []domain.Edge{
			{From: "a", To: "b", Weight: 0.7},
			{From: "a", To: "c", Weight: 0.3},
		},
	}
	w, err := NewWalker(g, 42)
	if err != nil {
		t.Fatalf("new walker: %v", err)
	}
	const n = 4000
	b := 0
	for i := 0; i < n; i++ {
		path, err := w.Walk("a", 1)
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		if len(path) != 2 {
			t.Fatalf("expected 2-step path, got %v", path)
		}
		if path[1] == "b" {
			b++
		}
	}
	frac := float64(b) / float64(n)
	if frac < 0.66 || frac > 0.74 {
		t.Fatalf("transition distribution off: b fraction = %.3f, want ~0.70", frac)
	}
}

func TestTermination(t *testing.T) {
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "a"}, {ID: "b"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1.0}},
	}
	w, _ := NewWalker(g, 7)
	path, err := w.Walk("a", 100)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// b is terminal, so the walk must stop at b regardless of maxSteps.
	if len(path) != 2 || path[1] != "b" {
		t.Fatalf("expected [a b], got %v", path)
	}
}

func TestStartWithUnmetDependency(t *testing.T) {
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "cart"}, {ID: "pay"}},
		Edges: []domain.Edge{{From: "cart", To: "pay", Weight: 1.0, Dependency: true}},
	}
	w, _ := NewWalker(g, 1)
	if _, err := w.Walk("pay", 5); err == nil {
		t.Fatal("expected error starting at node with unmet dependency")
	}
}

func TestStartNotInGraph(t *testing.T) {
	g := domain.ScenarioGraph{Nodes: []domain.Node{{ID: "a"}}}
	w, _ := NewWalker(g, 1)
	if _, err := w.Walk("zzz", 5); err == nil {
		t.Fatal("expected error for start node not in graph")
	}
}

func TestNewWalkerRejectsInvalidGraph(t *testing.T) {
	// Empty graph fails domain validation.
	if _, err := NewWalker(domain.ScenarioGraph{}, 1); err == nil {
		t.Fatal("expected error building walker for invalid graph")
	}
}
