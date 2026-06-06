// Package scenariofile turns a compact, human-authored scenario document into a
// full api.RunSpec. It exists to lower the barrier to a first run: instead of
// hand-writing the experiment, target, graph, templates and user list as
// separate JSON blobs, an operator writes one short file —
//
//	target: http://localhost:9000
//	flow:
//	  - id: browse
//	    request: GET /browse
//	  - id: cart
//	    request: POST /cart
//	    body: '{"qty":1}'
//	  - id: checkout
//	    request: POST /checkout
//	    body: '{"total":42}'
//	    dependsOn: cart
//
// and Expand fills in the rest with sensible defaults. The file is YAML or JSON;
// sigs.k8s.io/yaml parses both and honors the json struct tags, so field names
// match the rest of the codebase.
package scenariofile

import (
	"fmt"
	"net/url"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/chordpli/tmula/internal/api"
	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/scenario"
)

// Scenario is the compact, single-document description of a run. Only Target and
// Flow are required; everything else has a default (see Expand).
type Scenario struct {
	// Target is the system-under-test base URL (e.g. http://localhost:9000).
	Target string `json:"target"`
	// Allow lists hosts the run may reach. Empty defaults to the target's host.
	Allow []string `json:"allow,omitempty"`
	// Flow is the ordered list of steps a virtual user walks. Consecutive steps
	// are linked with a transition edge; a step's DependsOn marks that edge as a
	// required (never-skipped) dependency.
	Flow []Step `json:"flow"`
	// Users is the closed-model virtual-user count (default 20). Ignored when
	// Open is set, since the open model generates its own sessions.
	Users int `json:"users,omitempty"`
	// MaxSteps bounds each session's walk length (default: the flow length).
	MaxSteps int `json:"maxSteps,omitempty"`
	// Seed makes a run reproducible (default 1).
	Seed int64 `json:"seed,omitempty"`
	// Open, when set, switches the run to the open (arrival-rate) model.
	Open *Open `json:"open,omitempty"`
	// Segments is an optional persona mix (open model only).
	Segments []domain.Segment `json:"segments,omitempty"`
}

// Step is one node in the flow: an id, the request it makes, and how it links to
// the rest of the flow.
type Step struct {
	// ID names the node and its template; it must be unique within the flow.
	ID string `json:"id"`
	// Request is the shorthand "METHOD /path" the step calls, e.g. "GET /browse".
	// An empty Request makes the step a pure state node (no request).
	Request string `json:"request,omitempty"`
	// Body is the request payload template (sent as the template's payload).
	Body string `json:"body,omitempty"`
	// Headers are static request headers for this step.
	Headers map[string]string `json:"headers,omitempty"`
	// DependsOn is the id of an earlier step this one requires. The edge into
	// this step is marked as a dependency, so it is never skipped on deviation.
	DependsOn string `json:"dependsOn,omitempty"`
	// Weight is the probability of taking the edge to the next step (default 1).
	Weight float64 `json:"weight,omitempty"`
}

// Open parameterizes the open (arrival-rate) workload. ForSeconds is required;
// the rate is either a single constant Rate or a From→To ramp over RampSeconds.
type Open struct {
	Rate           float64 `json:"rate,omitempty"` // constant arrivals/sec
	From           float64 `json:"from,omitempty"` // ramp start rate
	To             float64 `json:"to,omitempty"`   // ramp peak rate
	RampSeconds    int     `json:"rampSeconds,omitempty"`
	HoldSeconds    int     `json:"holdSeconds,omitempty"`
	Shape          string  `json:"shape,omitempty"` // override: constant|ramp|spike|soak
	ForSeconds     int     `json:"forSeconds"`
	ThinkMs        []int   `json:"thinkMs,omitempty"` // [min, max]
	MaxConcurrency int     `json:"maxConcurrency,omitempty"`
}

// Parse decodes a scenario document (YAML or JSON — sigs.k8s.io/yaml handles
// both) into a Scenario. It does not expand or fully validate; call Expand.
func Parse(data []byte) (Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Scenario{}, fmt.Errorf("scenariofile: parse: %w", err)
	}
	return s, nil
}

// defaultRateCap is applied when the scenario does not constrain the target; it
// is generous enough not to throttle most local runs while still being a cap.
var defaultRateCap = domain.RateCap{MaxRPS: 10000, MaxConcurrency: 1000}

// Expand turns a Scenario into a complete api.RunSpec, filling every field the
// control plane needs with defaults derived from the flow. It returns an error
// if the scenario is missing something it cannot default (a target, a usable
// flow, or a malformed request line).
func Expand(s Scenario) (api.RunSpec, error) {
	if strings.TrimSpace(s.Target) == "" {
		return api.RunSpec{}, fmt.Errorf("scenariofile: target is required")
	}
	if len(s.Flow) == 0 {
		return api.RunSpec{}, fmt.Errorf("scenariofile: flow must have at least one step")
	}

	templates, err := buildTemplates(s.Flow)
	if err != nil {
		return api.RunSpec{}, err
	}
	graph, err := buildGraph(s.Flow)
	if err != nil {
		return api.RunSpec{}, err
	}
	// Validate the generated graph with the stricter scenario rules (transition
	// weights in [0,1], per-node outgoing sum <= 1, dependency edges form a DAG)
	// so a malformed flow is rejected here rather than running a skewed walk.
	if err := scenario.Validate(graph); err != nil {
		return api.RunSpec{}, fmt.Errorf("scenariofile: %w", err)
	}

	allow := s.Allow
	if len(allow) == 0 {
		host, err := hostOf(s.Target)
		if err != nil {
			return api.RunSpec{}, err
		}
		allow = []string{host}
	}

	seed := s.Seed
	if seed == 0 {
		seed = 1
	}
	maxSteps := s.MaxSteps
	if maxSteps <= 0 {
		maxSteps = len(s.Flow)
	}

	spec := api.RunSpec{
		Experiment: domain.Experiment{
			Name: "cli-run", TargetEnvID: "env", ScenarioGraphID: graph.ID,
			Params: domain.ExperimentParams{DeviationRate: 0, AuthStrategy: domain.CredPool},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL: s.Target, Allowlist: allow, RateCap: defaultRateCap, EnvClass: domain.EnvDev,
		},
		Graph:     graph,
		Templates: templates,
		Start:     domain.ID(s.Flow[0].ID),
		MaxSteps:  maxSteps,
		Seed:      seed,
	}

	if s.Open != nil {
		model, err := buildWorkload(*s.Open)
		if err != nil {
			return api.RunSpec{}, err
		}
		spec.Workload = &model
		spec.Segments = s.Segments
		// The open model generates its own sessions; a single identity suffices.
		spec.Users = []load.VirtualUser{{ID: "u0"}}
		spec.Experiment.Params.VirtualUserCount = 1
	} else {
		if len(s.Segments) > 0 {
			return api.RunSpec{}, fmt.Errorf("scenariofile: segments require an open workload")
		}
		n := s.Users
		if n <= 0 {
			n = 20
		}
		spec.Users = makeUsers(n)
		spec.Experiment.Params.VirtualUserCount = n
	}
	return spec, nil
}

// buildTemplates maps each request-bearing step to an API template keyed t_<id>.
func buildTemplates(flow []Step) (map[domain.ID]domain.APITemplate, error) {
	out := make(map[domain.ID]domain.APITemplate, len(flow))
	for _, st := range flow {
		if st.Request == "" {
			continue // pure state node
		}
		method, path, err := parseRequest(st.Request)
		if err != nil {
			return nil, fmt.Errorf("scenariofile: step %q: %w", st.ID, err)
		}
		out[templateID(st.ID)] = domain.APITemplate{
			ID:              templateID(st.ID),
			Protocol:        domain.ProtocolREST,
			Method:          method,
			Path:            path,
			Headers:         st.Headers,
			PayloadTemplate: st.Body,
		}
	}
	return out, nil
}

// buildGraph turns the flow into a scenario graph: nodes in order, a transition
// edge between consecutive steps, and a dependency edge wherever a step declares
// DependsOn (marked on the consecutive edge when adjacent, else added explicitly).
func buildGraph(flow []Step) (domain.ScenarioGraph, error) {
	seen := make(map[string]bool, len(flow))
	nodes := make([]domain.Node, 0, len(flow))
	for _, st := range flow {
		if st.ID == "" {
			return domain.ScenarioGraph{}, fmt.Errorf("scenariofile: every step needs an id")
		}
		if seen[st.ID] {
			return domain.ScenarioGraph{}, fmt.Errorf("scenariofile: duplicate step id %q", st.ID)
		}
		seen[st.ID] = true
		node := domain.Node{ID: domain.ID(st.ID)}
		if st.Request != "" {
			node.APITemplateID = templateID(st.ID)
		}
		nodes = append(nodes, node)
	}

	var edges []domain.Edge
	for i := 0; i < len(flow)-1; i++ {
		from, to := flow[i], flow[i+1]
		weight := from.Weight
		if weight == 0 {
			weight = 1
		}
		edges = append(edges, domain.Edge{
			From:       domain.ID(from.ID),
			To:         domain.ID(to.ID),
			Weight:     weight,
			Dependency: to.DependsOn == from.ID,
		})
	}
	// A DependsOn that does not point at the immediately preceding step becomes
	// an explicit required edge so the dependency is still enforced.
	for i, st := range flow {
		if st.DependsOn == "" {
			continue
		}
		if !seen[st.DependsOn] {
			return domain.ScenarioGraph{}, fmt.Errorf("scenariofile: step %q dependsOn unknown step %q", st.ID, st.DependsOn)
		}
		if i > 0 && flow[i-1].ID == st.DependsOn {
			continue // already marked on the consecutive edge above
		}
		// A precondition-only edge with weight 0: the walker records the
		// dependency from Dependency=true (not weight), and skips weight-0 edges
		// as transitions — so this enforces the precondition WITHOUT adding a
		// traversable shortcut that could skip the steps in between.
		edges = append(edges, domain.Edge{
			From: domain.ID(st.DependsOn), To: domain.ID(st.ID), Weight: 0, Dependency: true,
		})
	}
	return domain.ScenarioGraph{ID: "scenario", Nodes: nodes, Edges: edges}, nil
}

// buildWorkload maps the Open block onto a domain workload model, picking the
// arrival shape from the fields supplied (explicit Shape wins; then a From→To
// ramp; otherwise a constant Rate).
func buildWorkload(o Open) (domain.WorkloadModel, error) {
	if o.ForSeconds <= 0 {
		return domain.WorkloadModel{}, fmt.Errorf("scenariofile: open.forSeconds must be > 0")
	}
	shape := domain.RateShape(o.Shape)
	if shape == "" {
		if o.From > 0 || o.To > 0 {
			shape = domain.RateRamp
		} else {
			shape = domain.RateConstant
		}
	}
	arrival := domain.ArrivalProfile{
		Shape:       shape,
		StartRate:   o.From,
		PeakRate:    o.To,
		RampSeconds: o.RampSeconds,
		HoldSeconds: o.HoldSeconds,
	}
	// A constant scenario expresses its single rate as Rate; carry it into both
	// fields so the rate function and the back-pressure ceiling agree.
	if shape == domain.RateConstant && o.Rate > 0 {
		arrival.StartRate, arrival.PeakRate = o.Rate, o.Rate
	}
	if arrival.PeakRate == 0 && o.Rate > 0 {
		arrival.PeakRate = o.Rate
	}

	var think domain.ThinkTime
	switch len(o.ThinkMs) {
	case 0:
	case 2:
		think = domain.ThinkTime{MinMs: o.ThinkMs[0], MaxMs: o.ThinkMs[1]}
	default:
		return domain.WorkloadModel{}, fmt.Errorf("scenariofile: open.thinkMs must be [min, max]")
	}

	model := domain.WorkloadModel{
		Kind:            domain.WorkloadOpen,
		Arrival:         arrival,
		DurationSeconds: o.ForSeconds,
		MaxConcurrency:  o.MaxConcurrency,
		ThinkTime:       think,
	}
	if err := model.Validate(); err != nil {
		return domain.WorkloadModel{}, fmt.Errorf("scenariofile: %w", err)
	}
	return model, nil
}

// parseRequest splits a "METHOD /path" shorthand into its parts.
func parseRequest(req string) (method, path string, err error) {
	fields := strings.Fields(req)
	if len(fields) != 2 {
		return "", "", fmt.Errorf("request %q must be \"METHOD /path\"", req)
	}
	method = strings.ToUpper(fields[0])
	path = fields[1]
	if !strings.HasPrefix(path, "/") {
		return "", "", fmt.Errorf("request path %q must start with /", path)
	}
	return method, path, nil
}

// hostOf returns the host (without port) of a base URL, for the default allowlist.
func hostOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("scenariofile: target %q is not a valid URL", raw)
	}
	return u.Hostname(), nil
}

func templateID(stepID string) domain.ID { return domain.ID("t_" + stepID) }

func makeUsers(n int) []load.VirtualUser {
	users := make([]load.VirtualUser, n)
	for i := range users {
		users[i] = load.VirtualUser{ID: fmt.Sprintf("u%d", i)}
	}
	return users
}
