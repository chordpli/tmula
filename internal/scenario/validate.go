package scenario

import (
	"fmt"

	"github.com/chordpli/tmula/internal/domain"
)

const weightEpsilon = 1e-9

// Validate runs structural and semantic checks: the domain-level structural
// checks, transition-weight bounds (each in [0,1], per-node outgoing sum <= 1),
// and that dependency edges form a DAG.
func Validate(g domain.ScenarioGraph) error {
	if err := g.Validate(); err != nil {
		return err
	}
	if err := validateWeights(g); err != nil {
		return err
	}
	if _, err := TopoSortDependencies(g); err != nil {
		return err
	}
	return nil
}

func validateWeights(g domain.ScenarioGraph) error {
	sum := make(map[domain.ID]float64, len(g.Nodes))
	for _, e := range g.Edges {
		// Positive predicate so NaN is rejected (it fails every comparison)
		// instead of slipping through and poisoning the per-node sum below.
		if !(e.Weight >= 0 && e.Weight <= 1) {
			return fmt.Errorf("scenario: edge %s->%s weight %v out of range [0,1]", e.From, e.To, e.Weight)
		}
		sum[e.From] += e.Weight
	}
	for node, s := range sum {
		if s > 1+weightEpsilon {
			return fmt.Errorf("scenario: node %s outgoing transition weights sum to %v (> 1)", node, s)
		}
	}
	return nil
}

// TopoSortDependencies returns node IDs in dependency order: a required
// predecessor (an edge with Dependency=true) always precedes its dependents.
// Non-dependency edges do not constrain ordering. It errors if the dependency
// edges contain a cycle.
func TopoSortDependencies(g domain.ScenarioGraph) ([]domain.ID, error) {
	indeg := make(map[domain.ID]int, len(g.Nodes))
	adj := make(map[domain.ID][]domain.ID, len(g.Nodes))
	for _, n := range g.Nodes {
		indeg[n.ID] = 0
	}
	for _, e := range g.Edges {
		if !e.Dependency {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
	}

	// Kahn's algorithm, seeded in node declaration order for deterministic output.
	var queue []domain.ID
	for _, n := range g.Nodes {
		if indeg[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	order := make([]domain.ID, 0, len(g.Nodes))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, nb := range adj[cur] {
			indeg[nb]--
			if indeg[nb] == 0 {
				queue = append(queue, nb)
			}
		}
	}
	if len(order) != len(g.Nodes) {
		return nil, fmt.Errorf("scenario: dependency edges contain a cycle (ordered %d of %d nodes)", len(order), len(g.Nodes))
	}
	return order, nil
}
