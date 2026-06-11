package load

import (
	"context"
	"slices"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// chainGraph builds a linear scenario s0 -> s1 -> ... -> s{n-1} where every node
// makes a request, so an undeviated walk visits the full chain in order and any
// deviation (an abandon truncates it) is visible in the request sequence.
func chainGraph(n int) (domain.ScenarioGraph, map[domain.ID]domain.APITemplate) {
	g := domain.ScenarioGraph{ID: "chain"}
	tmpls := make(map[domain.ID]domain.APITemplate, n)
	for i := 0; i < n; i++ {
		id := domain.ID(string(rune('a' + i)))
		tid := domain.ID("t_" + string(id))
		g.Nodes = append(g.Nodes, domain.Node{ID: id, APITemplateID: tid})
		tmpls[tid] = domain.APITemplate{Method: "GET", Path: "/" + string(id)}
		if i > 0 {
			g.Edges = append(g.Edges, domain.Edge{From: g.Nodes[i-1].ID, To: id, Weight: 1})
		}
	}
	return g, tmpls
}

// sessionPath drives one RunSession to completion and returns the node ids it
// made requests to, in order. Every step must succeed.
func sessionPath(t *testing.T, r *Runner, g domain.ScenarioGraph, seed int64) []domain.ID {
	t.Helper()
	results, err := r.RunSession(context.Background(), g, "a", 10, VirtualUser{ID: "u"}, seed, nil)
	if err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	path := make([]domain.ID, 0, len(results))
	for _, sr := range results {
		if sr.Err != nil {
			t.Fatalf("unexpected step error: %+v", sr)
		}
		path = append(path, sr.NodeID)
	}
	return path
}

// TestRunSessionDeviationChangesPathDeterministically wires the deviation knob
// end to end: with rate 1.0 every step deviates (abandon or explore, 50:50 in
// the engine), so for this seed the session's request sequence differs from the
// undeviated happy path — and, being seeded, it is identical across repeats.
func TestRunSessionDeviationChangesPathDeterministically(t *testing.T) {
	g, tmpls := chainGraph(5)
	adapter := &fakeAdapter{}

	base := sessionPath(t, NewRunner(adapter, "http://t", tmpls), g, 1)
	want := []domain.ID{"a", "b", "c", "d", "e"}
	if !slices.Equal(base, want) {
		t.Fatalf("undeviated path = %v, want %v", base, want)
	}

	dev1 := sessionPath(t, NewRunner(adapter, "http://t", tmpls, WithDeviation(1.0)), g, 1)
	dev2 := sessionPath(t, NewRunner(adapter, "http://t", tmpls, WithDeviation(1.0)), g, 1)
	if !slices.Equal(dev1, dev2) {
		t.Errorf("deviated path not reproducible for a fixed seed: %v vs %v", dev1, dev2)
	}
	if slices.Equal(dev1, base) {
		t.Errorf("deviation rate 1.0 left the path unchanged: %v", dev1)
	}
}

// TestRunSessionZeroDeviationKeepsPath pins the compatibility contract: a rate
// of 0 (whether defaulted or explicit) takes the plain weighted Walk, so an
// undeviated run is byte-for-byte what it was before deviation was wired in.
func TestRunSessionZeroDeviationKeepsPath(t *testing.T) {
	g, tmpls := chainGraph(5)
	adapter := &fakeAdapter{}

	base := sessionPath(t, NewRunner(adapter, "http://t", tmpls), g, 42)
	zero := sessionPath(t, NewRunner(adapter, "http://t", tmpls, WithDeviation(0)), g, 42)
	if !slices.Equal(zero, base) {
		t.Errorf("deviation rate 0 changed the path: %v, want %v", zero, base)
	}
}
