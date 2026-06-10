package scenario

import (
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

const validJSON = `{
  "id": "checkout",
  "nodes": [
    {"id": "browse", "apiTemplateId": "t_browse"},
    {"id": "cart", "apiTemplateId": "t_cart"},
    {"id": "pay", "apiTemplateId": "t_pay"}
  ],
  "edges": [
    {"from": "browse", "to": "cart", "weight": 0.7, "dependency": false},
    {"from": "cart", "to": "pay", "weight": 0.9, "dependency": true}
  ]
}`

const validYAML = `
id: checkout
nodes:
  - id: browse
    apiTemplateId: t_browse
  - id: cart
    apiTemplateId: t_cart
  - id: pay
    apiTemplateId: t_pay
edges:
  - from: browse
    to: cart
    weight: 0.7
    dependency: false
  - from: cart
    to: pay
    weight: 0.9
    dependency: true
`

func TestParseJSONValid(t *testing.T) {
	g, err := Parse([]byte(validJSON), FormatJSON)
	if err != nil {
		t.Fatalf("parse valid json: %v", err)
	}
	if len(g.Nodes) != 3 || len(g.Edges) != 2 {
		t.Fatalf("unexpected graph shape: %d nodes, %d edges", len(g.Nodes), len(g.Edges))
	}
	if !g.Edges[1].Dependency {
		t.Error("cart->pay should be a dependency edge")
	}
}

func TestParseYAMLValid(t *testing.T) {
	g, err := Parse([]byte(validYAML), FormatYAML)
	if err != nil {
		t.Fatalf("parse valid yaml: %v", err)
	}
	if len(g.Nodes) != 3 || len(g.Edges) != 2 {
		t.Fatalf("yaml graph shape mismatch: %d nodes, %d edges", len(g.Nodes), len(g.Edges))
	}
	if g.Nodes[0].APITemplateID != "t_browse" {
		t.Errorf("yaml json-tag mapping failed: apiTemplateId = %q", g.Nodes[0].APITemplateID)
	}
}

func TestRoundTrip(t *testing.T) {
	g, err := Parse([]byte(validJSON), FormatJSON)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data, err := MarshalJSON(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	g2, err := Parse(data, FormatJSON)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(g2.Nodes) != len(g.Nodes) || len(g2.Edges) != len(g.Edges) || g2.ID != g.ID {
		t.Fatalf("round-trip mismatch: %+v vs %+v", g, g2)
	}
	if g2.Edges[1].Dependency != g.Edges[1].Dependency {
		t.Error("dependency flag lost in round-trip")
	}
}

func TestRejectWeightOverOne(t *testing.T) {
	bad := `{"id":"g","nodes":[{"id":"a"},{"id":"b"}],"edges":[{"from":"a","to":"b","weight":1.5}]}`
	if _, err := Parse([]byte(bad), FormatJSON); err == nil {
		t.Fatal("expected error for weight > 1")
	}
}

func TestRejectPerNodeWeightSumOverOne(t *testing.T) {
	bad := `{"id":"g","nodes":[{"id":"a"},{"id":"b"},{"id":"c"}],"edges":[
		{"from":"a","to":"b","weight":0.7},
		{"from":"a","to":"c","weight":0.6}]}`
	if _, err := Parse([]byte(bad), FormatJSON); err == nil {
		t.Fatal("expected error for per-node outgoing weight sum > 1")
	}
}

func TestRejectDependencyCycle(t *testing.T) {
	bad := `{"id":"g","nodes":[{"id":"a"},{"id":"b"}],"edges":[
		{"from":"a","to":"b","weight":0.5,"dependency":true},
		{"from":"b","to":"a","weight":0.5,"dependency":true}]}`
	if _, err := Parse([]byte(bad), FormatJSON); err == nil {
		t.Fatal("expected error for dependency cycle")
	}
}

func TestTopoSortOrder(t *testing.T) {
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []domain.Edge{
			{From: "a", To: "b", Weight: 1, Dependency: true},
			{From: "b", To: "c", Weight: 1, Dependency: true},
		},
	}
	order, err := TopoSortDependencies(g)
	if err != nil {
		t.Fatalf("topo sort: %v", err)
	}
	pos := map[domain.ID]int{}
	for i, id := range order {
		pos[id] = i
	}
	if !(pos["a"] < pos["b"] && pos["b"] < pos["c"]) {
		t.Fatalf("topo order violates dependencies: %v", order)
	}
}

func TestUnknownFormat(t *testing.T) {
	if _, err := Parse([]byte(`{}`), Format("toml")); err == nil {
		t.Fatal("expected error for unknown format")
	}
}
