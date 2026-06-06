package scenariofile

import (
	"testing"

	"github.com/chordpli/tmula/internal/domain"
)

const closedYAML = `
target: http://localhost:9000
flow:
  - id: browse
    request: GET /browse
  - id: cart
    request: POST /cart
    body: '{"qty":1}'
  - id: checkout
    request: POST /checkout
    body: '{"total":42}'
    dependsOn: cart
`

func TestParseAndExpandClosed(t *testing.T) {
	s, err := Parse([]byte(closedYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	// Defaults filled in.
	if spec.Start != "browse" {
		t.Errorf("start = %q, want browse", spec.Start)
	}
	if spec.Seed != 1 {
		t.Errorf("seed = %d, want default 1", spec.Seed)
	}
	if len(spec.Users) != 20 {
		t.Errorf("users = %d, want default 20", len(spec.Users))
	}
	if got := spec.TargetEnv.Allowlist; len(got) != 1 || got[0] != "localhost" {
		t.Errorf("allowlist = %v, want [localhost] (derived from target host)", got)
	}

	// Templates: one per request-bearing step, keyed t_<id>.
	if len(spec.Templates) != 3 {
		t.Fatalf("templates = %d, want 3", len(spec.Templates))
	}
	if tmpl := spec.Templates["t_cart"]; tmpl.Method != "POST" || tmpl.Path != "/cart" || tmpl.PayloadTemplate != `{"qty":1}` {
		t.Errorf("t_cart = %+v, want POST /cart with body", tmpl)
	}

	// Graph: 3 nodes, 2 consecutive edges, the cart->checkout edge is a dependency.
	if len(spec.Graph.Nodes) != 3 || len(spec.Graph.Edges) != 2 {
		t.Fatalf("graph nodes=%d edges=%d, want 3 and 2", len(spec.Graph.Nodes), len(spec.Graph.Edges))
	}
	var depFound bool
	for _, e := range spec.Graph.Edges {
		if e.From == "cart" && e.To == "checkout" {
			depFound = e.Dependency
		}
	}
	if !depFound {
		t.Error("cart->checkout edge should be a dependency (never skipped)")
	}

	// The spec the control plane receives must validate.
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded spec failed validation: %v", err)
	}
}

func TestExpandOpenWithSegments(t *testing.T) {
	s := Scenario{
		Target: "http://127.0.0.1:9000",
		Flow:   []Step{{ID: "browse", Request: "GET /browse"}},
		Open:   &Open{From: 20, To: 278, RampSeconds: 600, ForSeconds: 3600, ThinkMs: []int{200, 800}, MaxConcurrency: 20000},
		Segments: []domain.Segment{
			{Name: "browser", Weight: 0.7, Start: "browse"},
		},
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.Workload == nil || spec.Workload.Kind != domain.WorkloadOpen {
		t.Fatalf("workload = %+v, want open", spec.Workload)
	}
	if spec.Workload.Arrival.Shape != domain.RateRamp {
		t.Errorf("shape = %q, want ramp (from/to given)", spec.Workload.Arrival.Shape)
	}
	if spec.Workload.DurationSeconds != 3600 || spec.Workload.ThinkTime.MaxMs != 800 {
		t.Errorf("workload fields = %+v", spec.Workload)
	}
	if len(spec.Segments) != 1 {
		t.Errorf("segments = %d, want 1", len(spec.Segments))
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded open spec failed validation: %v", err)
	}
}

func TestExpandConstantOpen(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
		Open:   &Open{Rate: 50, ForSeconds: 10},
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.Workload.Arrival.Shape != domain.RateConstant || spec.Workload.Arrival.PeakRate != 50 {
		t.Errorf("arrival = %+v, want constant 50", spec.Workload.Arrival)
	}
}

func TestExpandRejects(t *testing.T) {
	cases := map[string]Scenario{
		"no target":  {Flow: []Step{{ID: "a", Request: "GET /a"}}},
		"empty flow": {Target: "http://h:1"},
		"segments without open": {Target: "http://h:1", Flow: []Step{{ID: "a", Request: "GET /a"}},
			Segments: []domain.Segment{{Name: "x", Weight: 1}}},
		"bad request line":   {Target: "http://h:1", Flow: []Step{{ID: "a", Request: "GET"}}},
		"path without slash": {Target: "http://h:1", Flow: []Step{{ID: "a", Request: "GET browse"}}},
		"duplicate id": {Target: "http://h:1", Flow: []Step{
			{ID: "a", Request: "GET /a"}, {ID: "a", Request: "GET /b"}}},
		"dependsOn unknown": {Target: "http://h:1", Flow: []Step{
			{ID: "a", Request: "GET /a"}, {ID: "b", Request: "GET /b", DependsOn: "ghost"}}},
		"open without duration": {Target: "http://h:1", Flow: []Step{{ID: "a", Request: "GET /a"}},
			Open: &Open{Rate: 10}},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Expand(s); err == nil {
				t.Errorf("expected an error for %q", name)
			}
		})
	}
}

func TestParseJSON(t *testing.T) {
	// JSON is valid YAML, so the same parser handles it.
	const j = `{"target":"http://h:1","flow":[{"id":"a","request":"GET /a"}],"users":5}`
	s, err := Parse([]byte(j))
	if err != nil {
		t.Fatalf("Parse json: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(spec.Users) != 5 {
		t.Errorf("users = %d, want 5", len(spec.Users))
	}
}
