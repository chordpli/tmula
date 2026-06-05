package engine

import (
	"fmt"
	"math/rand"

	"github.com/chordpli/tmula/internal/domain"
)

// Walker traverses a scenario graph as one virtual user would: it moves between
// nodes by transition weight while honoring dependency edges as hard
// preconditions — a node is only entered once every dependency predecessor has
// been visited, so a required step is never skipped.
type Walker struct {
	nodes    map[domain.ID]bool
	outgoing map[domain.ID][]domain.Edge
	depPreds map[domain.ID][]domain.ID
	rng      *rand.Rand
}

// NewWalker builds a Walker for the graph using a seeded RNG for reproducibility.
func NewWalker(g domain.ScenarioGraph, seed int64) (*Walker, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	w := &Walker{
		nodes:    make(map[domain.ID]bool, len(g.Nodes)),
		outgoing: make(map[domain.ID][]domain.Edge, len(g.Nodes)),
		depPreds: make(map[domain.ID][]domain.ID, len(g.Nodes)),
		rng:      rand.New(rand.NewSource(seed)),
	}
	for _, n := range g.Nodes {
		w.nodes[n.ID] = true
	}
	for _, e := range g.Edges {
		w.outgoing[e.From] = append(w.outgoing[e.From], e)
		if e.Dependency {
			w.depPreds[e.To] = append(w.depPreds[e.To], e.From)
		}
	}
	return w, nil
}

// canEnter reports whether node may be entered given the set of visited nodes:
// every dependency predecessor must already have been visited.
func (w *Walker) canEnter(node domain.ID, visited map[domain.ID]bool) bool {
	for _, p := range w.depPreds[node] {
		if !visited[p] {
			return false
		}
	}
	return true
}

// Walk produces a path starting at start, taking at most maxSteps transitions.
// It stops early when no eligible transition remains (a terminal node or all
// outgoing targets blocked by unmet dependencies).
func (w *Walker) Walk(start domain.ID, maxSteps int) ([]domain.ID, error) {
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

		var eligible []domain.Edge
		var total float64
		for _, e := range w.outgoing[cur] {
			if e.Weight <= 0 {
				continue
			}
			if w.canEnter(e.To, visited) {
				eligible = append(eligible, e)
				total += e.Weight
			}
		}
		if len(eligible) == 0 || total <= 0 || step == maxSteps {
			break
		}

		r := w.rng.Float64() * total
		next := eligible[len(eligible)-1].To
		var acc float64
		for _, e := range eligible {
			acc += e.Weight
			if r < acc {
				next = e.To
				break
			}
		}
		cur = next
	}
	return path, nil
}
