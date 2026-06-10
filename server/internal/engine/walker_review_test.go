package engine

import (
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

func TestNegativeMaxStepsRejected(t *testing.T) {
	w, _ := NewWalker(domain.ScenarioGraph{Nodes: []domain.Node{{ID: "a"}}}, 1)
	if _, err := w.Walk("a", -1); err == nil {
		t.Error("Walk with negative maxSteps must error")
	}
	if _, err := w.WalkWithDeviation("a", -1, DeviationPolicy{}); err == nil {
		t.Error("WalkWithDeviation with negative maxSteps must error")
	}
}
