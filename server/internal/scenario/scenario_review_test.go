package scenario

import (
	"math"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestRejectNaNWeight guards against a NaN weight slipping past the bounds check
// (NaN fails every comparison) and poisoning the per-node weight sum.
func TestRejectNaNWeight(t *testing.T) {
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "a"}, {ID: "b"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: math.NaN()}},
	}
	if err := Validate(g); err == nil {
		t.Fatal("a NaN edge weight must be rejected")
	}
}
