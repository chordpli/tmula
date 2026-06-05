package engine

import (
	"fmt"

	"github.com/chordpli/tmula/internal/domain"
)

// DeviationPolicy controls how often, and how, a virtual user departs from the
// weighted "happy path". Dependency edges are never violated regardless of
// policy — deviation only affects which permitted transition is taken (or
// whether the journey ends early).
type DeviationPolicy struct {
	Rate    float64 // probability per step of deviating, in [0,1]
	Abandon bool    // deviation may end the journey early
	Explore bool    // deviation may pick an unlikely (uniform) transition
}

// WalkWithDeviation behaves like Walk but injects probabilistic deviation. With
// probability Rate at each step it either abandons the journey or takes a
// uniformly-random eligible transition instead of the weighted one. A node is
// still only entered once its dependency predecessors are visited.
func (w *Walker) WalkWithDeviation(start domain.ID, maxSteps int, p DeviationPolicy) ([]domain.ID, error) {
	if maxSteps < 0 {
		return nil, fmt.Errorf("engine: maxSteps must be >= 0, got %d", maxSteps)
	}
	if !w.nodes[start] {
		return nil, fmt.Errorf("engine: start node %q not in graph", start)
	}
	visited := make(map[domain.ID]bool, len(w.nodes))
	if !w.canEnter(start, visited) {
		return nil, fmt.Errorf("engine: start node %q has unmet dependency predecessors", start)
	}

	path := make([]domain.ID, 0, maxSteps+1)
	cur := start
	for step := 0; step <= maxSteps; step++ {
		path = append(path, cur)
		visited[cur] = true

		eligible, total := w.eligible(cur, visited)
		if len(eligible) == 0 || total <= 0 || step == maxSteps {
			break
		}

		if p.Rate > 0 && w.rng.Float64() < p.Rate {
			if p.Abandon && (!p.Explore || w.rng.Float64() < 0.5) {
				break // user leaves mid-flow
			}
			if p.Explore {
				cur = eligible[w.rng.Intn(len(eligible))].To // unlikely path
				continue
			}
		}
		cur = weightedPick(eligible, total, w.rng)
	}
	return path, nil
}
