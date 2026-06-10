package domain

import (
	"math"
	"testing"
)

func TestScenarioGraphRejectsNonFiniteWeight(t *testing.T) {
	g := func(w float64) ScenarioGraph {
		return ScenarioGraph{
			Nodes: []Node{{ID: "a"}, {ID: "b"}},
			Edges: []Edge{{From: "a", To: "b", Weight: w}},
		}
	}
	for _, w := range []float64{math.Inf(1), math.NaN(), -1} {
		if err := g(w).Validate(); err == nil {
			t.Errorf("weight %v should be rejected", w)
		}
	}
	if err := g(0.5).Validate(); err != nil {
		t.Errorf("finite weight 0.5 should pass: %v", err)
	}
}

func TestWorkloadRejectsNonFiniteArrivalRate(t *testing.T) {
	base := WorkloadModel{
		Kind:            WorkloadOpen,
		DurationSeconds: 1,
		Arrival:         ArrivalProfile{Shape: RateConstant},
	}
	for _, r := range []float64{math.NaN(), math.Inf(1)} {
		m := base
		m.Arrival.PeakRate = r
		if err := m.Validate(); err == nil {
			t.Errorf("non-finite arrival rate %v should be rejected", r)
		}
	}
	ok := base
	ok.Arrival.PeakRate = 50
	if err := ok.Validate(); err != nil {
		t.Errorf("finite arrival rate 50 should pass: %v", err)
	}
}
