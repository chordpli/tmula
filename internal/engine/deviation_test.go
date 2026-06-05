package engine

import (
	"testing"

	"github.com/chordpli/tmula/internal/domain"
)

func depGraph() domain.ScenarioGraph {
	// browse -> cart -> pay, where pay depends on cart.
	return domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "browse"}, {ID: "cart"}, {ID: "pay"}},
		Edges: []domain.Edge{
			{From: "browse", To: "cart", Weight: 1.0},
			{From: "cart", To: "pay", Weight: 1.0, Dependency: true},
		},
	}
}

func TestDeviationNeverViolatesDependencies(t *testing.T) {
	w, _ := NewWalker(depGraph(), 99)
	policy := DeviationPolicy{Rate: 1.0, Abandon: true, Explore: true}
	for i := 0; i < 500; i++ {
		path, err := w.WalkWithDeviation("browse", 10, policy)
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		pCart, pPay := indexOf(path, "cart"), indexOf(path, "pay")
		if pPay >= 0 && (pCart < 0 || pCart > pPay) {
			t.Fatalf("run %d: dependency violated: %v", i, path)
		}
	}
}

func TestZeroRateEqualsWalk(t *testing.T) {
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 0.7}, {From: "a", To: "c", Weight: 0.3}},
	}
	w1, _ := NewWalker(g, 5)
	w2, _ := NewWalker(g, 5)
	policy := DeviationPolicy{Rate: 0, Abandon: true, Explore: true}
	for i := 0; i < 50; i++ {
		p1, _ := w1.Walk("a", 1)
		p2, _ := w2.WalkWithDeviation("a", 1, policy)
		if len(p1) != len(p2) || p1[len(p1)-1] != p2[len(p2)-1] {
			t.Fatalf("rate=0 should match Walk: %v vs %v", p1, p2)
		}
	}
}

func TestAbandonShortensJourneys(t *testing.T) {
	// Long optional chain a->b->c->d->e (no dependencies).
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"}},
		Edges: []domain.Edge{
			{From: "a", To: "b", Weight: 1},
			{From: "b", To: "c", Weight: 1},
			{From: "c", To: "d", Weight: 1},
			{From: "d", To: "e", Weight: 1},
		},
	}
	noDev, _ := NewWalker(g, 1)
	dev, _ := NewWalker(g, 1)
	full, _ := noDev.Walk("a", 10)
	if len(full) != 5 {
		t.Fatalf("happy path should visit all 5, got %v", full)
	}
	// With high abandon rate, average journey is shorter than the full path.
	shorter := 0
	for i := 0; i < 200; i++ {
		p, _ := dev.WalkWithDeviation("a", 10, DeviationPolicy{Rate: 0.8, Abandon: true})
		if len(p) < len(full) {
			shorter++
		}
	}
	if shorter == 0 {
		t.Fatal("abandon deviation never shortened any journey")
	}
}
