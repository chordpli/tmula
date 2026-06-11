package scenariofile

import (
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
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

func TestExpandCarriesStepExtractors(t *testing.T) {
	spec, err := Expand(Scenario{
		Target: "http://h:1",
		Flow: []Step{
			{ID: "products", Request: "GET /products", Extract: map[string]string{"productId": "items.0.id"}},
			{ID: "cart", Request: "POST /cart", Body: `{"productId":"{{.productId}}"}`},
		},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got := spec.Templates["t_products"].Extract["productId"]; got != "items.0.id" {
		t.Errorf("extract productId = %q, want items.0.id", got)
	}
}

// TestNonAdjacentDependencyDoesNotCreateShortcut guards the regression where a
// dependsOn pointing at a non-preceding step added a traversable forward edge,
// letting the walk skip the steps in between (~50% of the time).
func TestNonAdjacentDependencyDoesNotCreateShortcut(t *testing.T) {
	spec, err := Expand(Scenario{
		Target: "http://h:1",
		Flow: []Step{
			{ID: "a", Request: "GET /a"},
			{ID: "b", Request: "GET /b"},
			{ID: "c", Request: "GET /c", DependsOn: "a"}, // non-adjacent dependency
		},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	var outA float64
	var depRecorded bool
	for _, e := range spec.Graph.Edges {
		if e.From == "a" {
			outA += e.Weight
		}
		if e.From == "a" && e.To == "c" {
			if e.Weight > 0 {
				t.Errorf("phantom traversable edge a->c (weight %v) lets the walk skip b", e.Weight)
			}
			if e.Dependency {
				depRecorded = true
			}
		}
	}
	if outA > 1 {
		t.Errorf("node a outgoing weight sum = %v, want <= 1 (no skip shortcut)", outA)
	}
	if !depRecorded {
		t.Error("c's dependency precondition on a was not recorded")
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

const graphFirstYAML = `
target: http://localhost:9000
start: browse
maxSteps: 12
graph:
  id: learned
  nodes:
    - { id: browse,   apiTemplateId: t_browse }
    - { id: search,   apiTemplateId: t_search }
    - { id: checkout, apiTemplateId: t_checkout }
    - { id: exit }
  edges:
    - { from: browse, to: search,   weight: 0.7 }
    - { from: browse, to: exit,     weight: 0.3 }
    - { from: search, to: checkout, weight: 0.5, dependency: true }
    - { from: search, to: exit,     weight: 0.5 }
templates:
  t_browse:   { method: GET,  path: /browse }
  t_search:   { method: GET,  path: /search }
  t_checkout: { method: POST, path: /checkout, payloadTemplate: '{"total":42}' }
`

func TestParseAndExpandGraphFirst(t *testing.T) {
	s, err := Parse([]byte(graphFirstYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	if spec.Start != "browse" {
		t.Errorf("start = %q, want browse", spec.Start)
	}
	if spec.MaxSteps != 12 {
		t.Errorf("maxSteps = %d, want 12", spec.MaxSteps)
	}
	if len(spec.Graph.Nodes) != 4 || len(spec.Graph.Edges) != 4 {
		t.Fatalf("graph nodes=%d edges=%d, want 4 and 4", len(spec.Graph.Nodes), len(spec.Graph.Edges))
	}
	var dep bool
	for _, e := range spec.Graph.Edges {
		if e.From == "search" && e.To == "checkout" {
			dep = e.Dependency
		}
	}
	if !dep {
		t.Error("search->checkout edge should keep its dependency flag")
	}
	// Map-form templates are normalized: the key becomes the id, protocol
	// defaults to rest.
	tmpl, ok := spec.Templates["t_checkout"]
	if !ok {
		t.Fatal("t_checkout template missing")
	}
	if tmpl.ID != "t_checkout" || tmpl.Protocol != domain.ProtocolREST {
		t.Errorf("t_checkout = %+v, want id and rest protocol filled from the map key", tmpl)
	}
	if tmpl.Method != "POST" || tmpl.Path != "/checkout" || tmpl.PayloadTemplate != `{"total":42}` {
		t.Errorf("t_checkout = %+v, want POST /checkout with body", tmpl)
	}
	if got := spec.TargetEnv.Allowlist; len(got) != 1 || got[0] != "localhost" {
		t.Errorf("allowlist = %v, want [localhost]", got)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded spec failed validation: %v", err)
	}
}

func TestGraphFirstDefaultsMaxStepsToNodeCount(t *testing.T) {
	s, err := Parse([]byte(graphFirstYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s.MaxSteps = 0
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.MaxSteps != len(spec.Graph.Nodes) {
		t.Errorf("maxSteps = %d, want node count %d", spec.MaxSteps, len(spec.Graph.Nodes))
	}
}

func TestGraphFirstWithOpenWorkload(t *testing.T) {
	s, err := Parse([]byte(graphFirstYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s.Open = &Open{Rate: 100, ForSeconds: 30, ThinkMs: []int{200, 800}}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.Workload == nil || spec.Workload.Arrival.PeakRate != 100 {
		t.Fatalf("workload = %+v, want constant open 100/s", spec.Workload)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded spec failed validation: %v", err)
	}
}

func TestGraphFirstRejects(t *testing.T) {
	graph := func() *domain.ScenarioGraph {
		return &domain.ScenarioGraph{
			ID: "g",
			Nodes: []domain.Node{
				{ID: "a", APITemplateID: "t_a"},
				{ID: "exit"},
			},
			Edges: []domain.Edge{{From: "a", To: "exit", Weight: 1}},
		}
	}
	templates := func() map[domain.ID]domain.APITemplate {
		return map[domain.ID]domain.APITemplate{
			"t_a": {Method: "GET", Path: "/a"},
		}
	}

	cases := map[string]Scenario{
		"graph and flow are exclusive": {
			Target: "http://h:1", Graph: graph(), Templates: templates(), Start: "a",
			Flow: []Step{{ID: "x", Request: "GET /x"}},
		},
		"graph needs a start": {
			Target: "http://h:1", Graph: graph(), Templates: templates(),
		},
		"start must be a graph node": {
			Target: "http://h:1", Graph: graph(), Templates: templates(), Start: "ghost",
		},
		"node template must exist": {
			Target: "http://h:1", Start: "a", Templates: map[domain.ID]domain.APITemplate{},
			Graph: graph(),
		},
		"graph must pass scenario validation": {
			Target: "http://h:1", Start: "a", Templates: templates(),
			Graph: &domain.ScenarioGraph{
				ID:    "g",
				Nodes: []domain.Node{{ID: "a", APITemplateID: "t_a"}},
				Edges: []domain.Edge{{From: "a", To: "a", Weight: 2}}, // weight > 1
			},
		},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Expand(s); err == nil {
				t.Errorf("expected an error for %q", name)
			}
		})
	}
}

func TestExpandCarriesMetrics(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
		Metrics: &Metrics{
			Prometheus: "http://prom:9090",
			Queries:    []domain.MetricQuery{{Name: "cpu", Query: "node_cpu"}},
		},
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.Metrics == nil || spec.Metrics.PrometheusURL != "http://prom:9090" || len(spec.Metrics.Queries) != 1 {
		t.Errorf("spec.Metrics = %+v, want the mapped source", spec.Metrics)
	}

	s.Metrics.Queries = nil // invalid: a source needs at least one query
	if _, err := Expand(s); err == nil {
		t.Error("expected an error for a metrics block without queries")
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
