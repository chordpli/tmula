// Package scenariofile turns a compact, human-authored scenario document into a
// full runspec.RunSpec. It exists to lower the barrier to a first run: instead of
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
	"context"
	"fmt"
	"net/url"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/chordpli/tmula/server/internal/runspec"
	"github.com/chordpli/tmula/server/internal/scenario"
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
	Flow []Step `json:"flow,omitempty"`
	// Graph, when set, supplies a full branching behavior graph instead of the
	// linear Flow (the two are mutually exclusive). Templates and Start must
	// accompany it. This graph-first form is what `tmula init` emits for learned
	// traffic, where the journey branches and a linear flow would lose it.
	Graph *domain.ScenarioGraph `json:"graph,omitempty"`
	// Templates maps template ids onto the request each graph node sends
	// (graph-first form only). A value may omit its ID and Protocol; they
	// default to the map key and "rest", matching the RunSpec template map.
	Templates map[domain.ID]domain.APITemplate `json:"templates,omitempty"`
	// Start is the node every session begins at. Required with Graph; with a
	// linear Flow it defaults to the first step.
	Start string `json:"start,omitempty"`
	// Users is the closed-model virtual-user count (default 20). Ignored when
	// Open is set, since the open model generates its own sessions.
	Users int `json:"users,omitempty"`
	// MaxSteps bounds each session's walk length (default: the flow length).
	MaxSteps int `json:"maxSteps,omitempty"`
	// Seed makes a run reproducible (default 1).
	Seed int64 `json:"seed,omitempty"`
	// DeviationRate is the per-step probability (0..1) that a virtual user
	// departs from the weighted happy path — abandoning the journey early or
	// exploring an unlikely transition — instead of following the scripted
	// weights. Dependency edges are never violated. Default 0: every user
	// follows the happy path, exactly as before.
	DeviationRate float64 `json:"deviationRate,omitempty"`
	// Open, when set, switches the run to the open (arrival-rate) model.
	Open *Open `json:"open,omitempty"`
	// Segments is an optional persona mix (open model only).
	Segments []domain.Segment `json:"segments,omitempty"`
	// Auth, when set, authenticates the run: each virtual user (closed) or session
	// (open) is assigned a credential by index so the simulated traffic carries
	// real auth material. Omit it to run unauthenticated (the default).
	Auth *Auth `json:"auth,omitempty"`
	// Metrics, when set, correlates the run with server-side Prometheus series:
	// each named query is fetched over the run's window and shown in the report
	// beside the client-side stats. The Prometheus host must be allowlisted.
	Metrics *Metrics `json:"metrics,omitempty"`
	// Findings, when set, tunes the thresholds that classify the run's
	// observations into findings: errorRate (0..1], p95LatencyMs (the gate is
	// disabled when omitted) and availabilityStreak (consecutive failures on
	// one API). Omit the block — or any field — to keep the defaults.
	Findings *obs.FindingConfig `json:"findings,omitempty"`
}

// Metrics is the compact server-metrics block: a Prometheus base URL and the
// named PromQL queries to fetch over the run's window.
type Metrics struct {
	// Prometheus is the Prometheus base URL (e.g. http://localhost:9090).
	Prometheus string `json:"prometheus"`
	// Queries names the PromQL expressions to correlate with the run.
	Queries []domain.MetricQuery `json:"queries"`
}

// Auth supplies the run's credentials in the compact file. It carries the secret
// in its own Token field (domain.Credential's secret is json:"-" and would never
// round-trip through YAML), and Expand maps it onto a domain.CredentialPool — so
// the file can hand-author tokens while the domain type keeps masking the secret
// at rest. Only the pre-supplied "pool" strategy is supported here; bootstrap
// signup is a follow-up that needs a signup transport this path does not wire.
//
// Credentials come from one of two places: inline Users, or an external Source (a
// file beside the scenario, or an environment variable). They are mutually
// exclusive. A Source keeps the secrets out of the document entirely — Expand
// reads it at expand time and resolves it into the pool's Entries, so the token
// still never round-trips through YAML.
type Auth struct {
	// Strategy selects how users obtain credentials; it defaults to "pool". "pool"
	// (pre-supplied entries or a source) and "login" (mint a token from a login
	// flow) are accepted; bootstrap-signup is a follow-up.
	Strategy string `json:"strategy,omitempty"`
	// Users is the pool of pre-supplied credentials, assigned to virtual users by
	// index (wrapping around when there are more users than entries).
	Users []Credential `json:"users,omitempty"`
	// Source, when set, names an external credential pool instead of inlining
	// Users: a file (resolved against the scenario file's directory) or an
	// environment variable, in an explicit format. Mutually exclusive with Users.
	Source *AuthSource `json:"source,omitempty"`
	// Login, when set (strategy "login"), describes how to mint a token: a
	// standalone login flow plus which response captures become the token (and
	// subject), and an optional scope. The token is minted at run time and never
	// authored in the file, so a login block carries no secret.
	Login *AuthLogin `json:"login,omitempty"`
	// Signup, when set (strategy "bootstrap-signup"), describes how to provision one
	// real account per virtual user up front: a signup flow, which captures become
	// the credential, and an optional teardown flow. It carries no secret — the token
	// is captured from the live signup response.
	Signup *AuthSignup `json:"signup,omitempty"`
	// KeepAccounts opts a bootstrap-signup run out of teardown, leaving the
	// provisioned accounts in place. It is the only escape from the gating-safety
	// rule that a bootstrap pool without a teardown flow is rejected.
	KeepAccounts bool `json:"keepAccounts,omitempty"`
}

// AuthSignup authors a bootstrap-signup credential strategy: a signup journey, an
// optional teardown journey, and the captures that become the minted credential. It
// carries no secret — the token comes from the live signup response.
type AuthSignup struct {
	// Flow is the ordered signup journey (usually a single POST to a registration
	// endpoint). Each step's extract captures response fields; capture names which of
	// those becomes the token (and subject).
	Flow []Step `json:"flow,omitempty"`
	// Teardown is the optional deprovision journey, run once per provisioned account
	// after the run. Each step can template the account's {{.subject}} so a
	// "DELETE /accounts/{{.subject}}" removes the exact account. Omit it (and set
	// keepAccounts) to leave accounts in place.
	Teardown []Step `json:"teardown,omitempty"`
	// Capture maps the credential fields to captured variable names: token (the
	// secret) and subject (the account id, needed for a {{.subject}}-templated
	// teardown). Both are optional — an empty token means tmula auto-detects the
	// token from the signup response.
	Capture AuthCapture `json:"capture"`
	// Start overrides the signup flow's start node (defaults to the first step).
	Start string `json:"start,omitempty"`
	// TeardownStart overrides the teardown flow's start node (defaults to the first
	// teardown step).
	TeardownStart string `json:"teardownStart,omitempty"`
}

// AuthLogin authors a login (token-minting) credential strategy: a standalone
// login flow (its own list of steps, exactly like the main flow), the captures
// that become the credential, and an optional scope. It carries no secret — the
// token comes from the live login response.
type AuthLogin struct {
	// Flow is the ordered login journey (usually a single POST to a login/token
	// endpoint). Each step's extract captures response fields; capture names which
	// of those becomes the token (and subject). It is a sibling of the main flow,
	// never a node in it, so the simulated traffic never observes the login.
	Flow []Step `json:"flow,omitempty"`
	// Capture maps the credential fields to captured variable names: token (the
	// secret) and subject (the principal id). Both are optional — an empty token
	// means tmula auto-detects the token from the login response.
	Capture AuthCapture `json:"capture"`
	// Scope is per-user (default) — one token per virtual user — or shared — one
	// client_credentials token for every session.
	Scope string `json:"scope,omitempty"`
	// Start overrides the login flow's start node (defaults to the first step).
	Start string `json:"start,omitempty"`
}

// AuthCapture names the captured variables that become the minted credential.
type AuthCapture struct {
	// Token is the captured variable that becomes the credential's secret (the
	// bearer token). Optional: when empty, tmula auto-detects the token from the
	// login/signup response (the common access_token/token/jwt/session shapes), so
	// an author need not name it explicitly.
	Token string `json:"token"`
	// Subject is the captured variable that becomes the non-sensitive subject.
	// Optional.
	Subject string `json:"subject,omitempty"`
}

// AuthSource names where an external credential pool lives. Exactly one of File
// or Env is set; Format declares how the body is encoded (csv|jsonl|tokens). It
// carries no secret — the secret lives only in the referenced file or variable.
type AuthSource struct {
	// File is a path to the credential file, resolved against the scenario file's
	// directory (the FileSource root). Mutually exclusive with Env.
	File string `json:"file,omitempty"`
	// Env names an environment variable holding the credential body. Mutually
	// exclusive with File.
	Env string `json:"env,omitempty"`
	// Format is the body encoding: csv (subject,token header), jsonl
	// ({subject,token} per line) or tokens (one secret per line).
	Format string `json:"format,omitempty"`
}

// Credential is one pre-supplied principal in the compact file: a non-sensitive
// subject and its secret token. It is distinct from domain.Credential precisely
// so the token can be authored in the file (the domain type hides its secret from
// serialization); Expand copies Token into the domain credential's secret.
type Credential struct {
	// Subject is the non-sensitive principal id (e.g. a username), exposed to
	// templates as {{.subject}}.
	Subject string `json:"subject,omitempty"`
	// Token is the secret auth material (e.g. a JWT), exposed to templates as
	// {{.token}}. It lives only in the authored file; the domain credential it
	// maps to never serializes its secret.
	Token string `json:"token,omitempty"`
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
	// Extract maps response JSON paths onto session variables for later steps.
	Extract map[string]string `json:"extract,omitempty"`
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

// Expand turns a Scenario into a complete runspec.RunSpec, filling every field the
// control plane needs with defaults derived from the flow. It returns an error
// if the scenario is missing something it cannot default (a target, a usable
// flow, or a malformed request line).
//
// A file-backed auth source is resolved against the process working directory.
// Callers that loaded the scenario from a file should use ExpandFrom with that
// file's directory so a relative source path resolves predictably (least
// surprise) and is confined there.
func Expand(s Scenario) (runspec.RunSpec, error) {
	return ExpandFrom(s, "")
}

// ExpandFrom is Expand with an explicit base directory for resolving a
// file-backed auth source. dir is the FileSource root: an operator-supplied
// relative path in auth.source.file is confined to it. An empty dir falls back to
// the process working directory.
func ExpandFrom(s Scenario, dir string) (runspec.RunSpec, error) {
	return expandFrom(s, dir, false)
}

// ExpandRef is ExpandFrom that leaves an external auth SOURCE unresolved: the
// pool carries its reference-only domain.CredentialSourceRef (file/env + format,
// never a secret) instead of loaded entries, so the reference can cross to a
// distributed engine whose workers resolve it locally. Inline users, login and
// bootstrap auth expand identically to ExpandFrom — only a source pool differs.
// It is the seam `tmula run --engine` uses to fan an authenticated run out
// without reading (or even needing) the credential file on the CLI host.
func ExpandRef(s Scenario, dir string) (runspec.RunSpec, error) {
	return expandFrom(s, dir, true)
}

// expandFrom is the shared expander. keepSourceRef ships an external auth source
// as an unresolved reference (for a distributed engine) instead of resolving it
// into entries (the single-node default).
func expandFrom(s Scenario, dir string, keepSourceRef bool) (runspec.RunSpec, error) {
	if strings.TrimSpace(s.Target) == "" {
		return runspec.RunSpec{}, fmt.Errorf("scenariofile: target is required")
	}

	var (
		graph     domain.ScenarioGraph
		templates map[domain.ID]domain.APITemplate
		start     string
		defSteps  int
		err       error
	)
	switch {
	case s.Graph != nil:
		if len(s.Flow) > 0 {
			return runspec.RunSpec{}, fmt.Errorf("scenariofile: graph and flow are mutually exclusive; author one journey form")
		}
		graph, templates, start, err = expandGraphFirst(s)
		if err != nil {
			return runspec.RunSpec{}, err
		}
		defSteps = len(graph.Nodes)
	case len(s.Flow) > 0:
		templates, err = buildTemplates(s.Flow)
		if err != nil {
			return runspec.RunSpec{}, err
		}
		graph, err = buildGraph(s.Flow)
		if err != nil {
			return runspec.RunSpec{}, err
		}
		start = s.Flow[0].ID
		if s.Start != "" {
			start = s.Start
		}
		defSteps = len(s.Flow)
	default:
		return runspec.RunSpec{}, fmt.Errorf("scenariofile: flow must have at least one step (or supply a graph)")
	}
	// Validate the graph with the stricter scenario rules (transition weights in
	// [0,1], per-node outgoing sum <= 1, dependency edges form a DAG) so a
	// malformed document is rejected here rather than running a skewed walk.
	if err := scenario.Validate(graph); err != nil {
		return runspec.RunSpec{}, fmt.Errorf("scenariofile: %w", err)
	}
	if !nodeExists(graph, start) {
		return runspec.RunSpec{}, fmt.Errorf("scenariofile: start node %q is not in the graph", start)
	}

	allow := s.Allow
	if len(allow) == 0 {
		host, err := hostOf(s.Target)
		if err != nil {
			return runspec.RunSpec{}, err
		}
		allow = []string{host}
	}

	// Reject a malformed deviation rate here with a scenariofile-prefixed message
	// rather than deferring to the spec's experiment validation downstream.
	if s.DeviationRate < 0 || s.DeviationRate > 1 {
		return runspec.RunSpec{}, fmt.Errorf("scenariofile: deviationRate %v out of range [0,1]", s.DeviationRate)
	}

	// Same early rejection for the findings block: a threshold the classifier
	// could never apply fails here with a scenariofile-prefixed message.
	if err := s.Findings.Validate(); err != nil {
		return runspec.RunSpec{}, fmt.Errorf("scenariofile: %w", err)
	}

	seed := s.Seed
	if seed == 0 {
		seed = 1
	}
	maxSteps := s.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defSteps
	}

	spec := runspec.RunSpec{
		Experiment: domain.Experiment{
			Name: "cli-run", TargetEnvID: "env", ScenarioGraphID: graph.ID,
			Params: domain.ExperimentParams{DeviationRate: s.DeviationRate, AuthStrategy: domain.CredPool},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL: s.Target, Allowlist: allow, RateCap: defaultRateCap, EnvClass: domain.EnvDev,
		},
		Graph:     graph,
		Templates: templates,
		Start:     domain.ID(start),
		MaxSteps:  maxSteps,
		Seed:      seed,
		// Findings passes through untouched (nil keeps the classifier defaults);
		// the run path resolves it into the ClassifyConfig at execution time.
		Findings: s.Findings,
	}

	if s.Open != nil {
		model, err := buildWorkload(*s.Open)
		if err != nil {
			return runspec.RunSpec{}, err
		}
		spec.Workload = &model
		spec.Segments = s.Segments
		// The open model generates its own sessions; a single identity suffices.
		spec.Users = []load.VirtualUser{{ID: "u0"}}
		spec.Experiment.Params.VirtualUserCount = 1
	} else {
		if len(s.Segments) > 0 {
			return runspec.RunSpec{}, fmt.Errorf("scenariofile: segments require an open workload")
		}
		n := s.Users
		if n <= 0 {
			n = 20
		}
		spec.Users = makeUsers(n)
		spec.Experiment.Params.VirtualUserCount = n
	}

	if s.Auth != nil {
		pool, loginFlow, err := buildCredentialPool(*s.Auth, dir, keepSourceRef)
		if err != nil {
			return runspec.RunSpec{}, err
		}
		spec.CredentialPool = &pool
		spec.LoginFlow = loginFlow
		spec.Experiment.Params.AuthStrategy = pool.Strategy
	}

	if s.Metrics != nil {
		src := domain.MetricsSource{PrometheusURL: s.Metrics.Prometheus, Queries: s.Metrics.Queries}
		if err := src.Validate(); err != nil {
			return runspec.RunSpec{}, fmt.Errorf("scenariofile: %w", err)
		}
		spec.Metrics = &src
	}
	return spec, nil
}

// expandGraphFirst validates the graph-first form (an explicit graph + a
// template map + a start node) and normalizes its template map: a value may
// omit its ID and Protocol, which default to the map key and "rest" — the same
// convention as the RunSpec template map, so a learned or hand-authored
// document round-trips into the web console unchanged.
func expandGraphFirst(s Scenario) (domain.ScenarioGraph, map[domain.ID]domain.APITemplate, string, error) {
	if strings.TrimSpace(s.Start) == "" {
		return domain.ScenarioGraph{}, nil, "", fmt.Errorf("scenariofile: start is required with a graph")
	}
	templates := make(map[domain.ID]domain.APITemplate, len(s.Templates))
	for id, t := range s.Templates {
		if t.ID == "" {
			t.ID = id
		}
		if t.Protocol == "" {
			t.Protocol = domain.ProtocolREST
		}
		templates[id] = t
	}
	for _, n := range s.Graph.Nodes {
		if n.APITemplateID == "" {
			continue // terminal node (done / exit)
		}
		if _, ok := templates[n.APITemplateID]; !ok {
			return domain.ScenarioGraph{}, nil, "", fmt.Errorf("scenariofile: node %q references unknown template %q", n.ID, n.APITemplateID)
		}
	}
	return *s.Graph, templates, s.Start, nil
}

// nodeExists reports whether the graph declares a node with the given id.
func nodeExists(g domain.ScenarioGraph, id string) bool {
	for _, n := range g.Nodes {
		if n.ID == domain.ID(id) {
			return true
		}
	}
	return false
}

// buildCredentialPool maps the compact Auth block onto a domain.CredentialPool.
// The strategy defaults to "pool" and only "pool" is accepted here (bootstrap
// signup is a follow-up that needs a signup transport this path does not wire).
//
// Credentials come from one of two mutually-exclusive places. Inline Users copy
// each authored Token into a domain credential's secret. An external Source (a
// file under dir, or an environment variable) is resolved HERE — this is the
// single resolution point: the source is loaded into a plain Entries-based pool
// (Strategy=pool, Source nil), so a CLI run always carries real credentials and a
// still-unresolved Source never reaches the run path. Either way the domain type
// keeps the secret out of any serialization.
func buildCredentialPool(a Auth, dir string, keepSourceRef bool) (domain.CredentialPool, *runspec.LoginFlowSpec, error) {
	strategy := domain.CredentialStrategy(a.Strategy)
	if a.Strategy == "" {
		strategy = domain.CredPool
	}
	switch strategy {
	case domain.CredPool:
		pool, err := buildPoolCredentials(a, dir, keepSourceRef)
		return pool, nil, err
	case domain.CredLogin:
		return buildLoginCredentials(a)
	case domain.CredBootstrapSignup:
		pool, err := buildBootstrapCredentials(a)
		return pool, nil, err
	default:
		return domain.CredentialPool{}, nil, fmt.Errorf("scenariofile: auth strategy %q is not supported (use %q with pre-supplied users or a source, %q with a login flow, or %q with a signup flow)", strategy, domain.CredPool, domain.CredLogin, domain.CredBootstrapSignup)
	}
}

// buildPoolCredentials maps the inline-users or source form onto a plain pool.
// When keepSourceRef is set, an external source is carried as an unresolved
// reference (pool.Source) instead of being loaded into entries, so the reference
// — never the secrets, and without needing the file locally — can cross to a
// distributed engine whose workers resolve it. Inline users are unaffected.
func buildPoolCredentials(a Auth, dir string, keepSourceRef bool) (domain.CredentialPool, error) {
	if a.Login != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth.login is only valid with the %q strategy", domain.CredLogin)
	}
	hasUsers, hasSource := len(a.Users) > 0, a.Source != nil
	if hasUsers && hasSource {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth takes either inline users or a source, not both")
	}
	if !hasUsers && !hasSource {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth needs inline users or a source for the %q strategy", domain.CredPool)
	}

	if hasSource && keepSourceRef {
		// Ship the reference unresolved (distributed engine path): validate its
		// shape but do not read it — the engine's workers load it locally.
		ref := domain.CredentialSourceRef{File: a.Source.File, Env: a.Source.Env, Format: a.Source.Format}
		if err := ref.Validate(); err != nil {
			return domain.CredentialPool{}, fmt.Errorf("scenariofile: %w", err)
		}
		return domain.CredentialPool{ID: "cli-pool", Strategy: domain.CredPool, Source: &ref}, nil
	}

	var entries []domain.Credential
	if hasUsers {
		entries = make([]domain.Credential, len(a.Users))
		for i, c := range a.Users {
			entries[i] = domain.Credential{Subject: c.Subject, Secret: c.Token}
		}
	} else {
		src, err := credentialSourceFor(*a.Source, dir)
		if err != nil {
			return domain.CredentialPool{}, err
		}
		entries, err = src.Load(context.Background())
		if err != nil {
			return domain.CredentialPool{}, fmt.Errorf("scenariofile: %w", err)
		}
	}
	return domain.CredentialPool{ID: "cli-pool", Strategy: domain.CredPool, Entries: entries}, nil
}

// buildLoginCredentials compiles the login authoring block into a login pool plus
// the standalone login flow the orchestrator mints tokens from. The login flow is
// built with the SAME buildGraph/buildTemplates helpers the main flow uses, so a
// login journey is authored exactly like any other flow, and it never carries a
// secret (the token is minted at run time).
func buildLoginCredentials(a Auth) (domain.CredentialPool, *runspec.LoginFlowSpec, error) {
	if len(a.Users) > 0 || a.Source != nil {
		return domain.CredentialPool{}, nil, fmt.Errorf("scenariofile: the %q strategy mints tokens from a login flow and takes no inline users or source", domain.CredLogin)
	}
	if a.Login == nil || len(a.Login.Flow) == 0 {
		return domain.CredentialPool{}, nil, fmt.Errorf("scenariofile: the %q strategy needs an auth.login.flow describing how to mint a token", domain.CredLogin)
	}
	// auth.login.capture.token is OPTIONAL: an empty token means tmula auto-detects
	// the token from the login response, so a login block need not name a capture.

	templates, err := buildTemplates(a.Login.Flow)
	if err != nil {
		return domain.CredentialPool{}, nil, err
	}
	graph, err := buildGraph(a.Login.Flow)
	if err != nil {
		return domain.CredentialPool{}, nil, err
	}
	graph.ID = "login"
	if err := scenario.Validate(graph); err != nil {
		return domain.CredentialPool{}, nil, fmt.Errorf("scenariofile: login flow: %w", err)
	}
	start := a.Login.Flow[0].ID
	if a.Login.Start != "" {
		start = a.Login.Start
	}
	if !nodeExists(graph, start) {
		return domain.CredentialPool{}, nil, fmt.Errorf("scenariofile: login flow start node %q is not in the login flow", start)
	}

	scope := domain.LoginScope(a.Login.Scope)
	if a.Login.Scope != "" && !scope.Valid() {
		return domain.CredentialPool{}, nil, fmt.Errorf("scenariofile: auth.login.scope %q is not valid (want %q or %q)", a.Login.Scope, domain.LoginPerUser, domain.LoginShared)
	}

	flowID := domain.ID("login")
	pool := domain.CredentialPool{
		ID:          "cli-pool",
		Strategy:    domain.CredLogin,
		LoginFlowID: &flowID,
		LoginScope:  scope,
	}
	loginFlow := &runspec.LoginFlowSpec{
		Graph:      graph,
		Templates:  templates,
		Start:      domain.ID(start),
		MaxSteps:   len(a.Login.Flow),
		TokenVar:   a.Login.Capture.Token,
		SubjectVar: a.Login.Capture.Subject,
	}
	return pool, loginFlow, nil
}

// buildBootstrapCredentials maps the signup authoring block onto a bootstrap-signup
// pool carrying a declarative domain.SignupFlow (signup steps + a capture mapping +
// an optional teardown journey). It carries no secret — the token is captured at run
// time from the live signup response. The pool's full validation (a resolvable token
// capture, well-formed steps, and the gating-safety teardown-or-keepAccounts rule)
// runs in domain.CredentialPool.Validate / runspec.Validate; this builder only
// translates the authored steps into the domain shape.
func buildBootstrapCredentials(a Auth) (domain.CredentialPool, error) {
	if len(a.Users) > 0 || a.Source != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: the %q strategy provisions accounts from a signup flow and takes no inline users or source", domain.CredBootstrapSignup)
	}
	if a.Login != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth.login is only valid with the %q strategy", domain.CredLogin)
	}
	if a.Signup == nil || len(a.Signup.Flow) == 0 {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: the %q strategy needs an auth.signup.flow describing how to provision an account", domain.CredBootstrapSignup)
	}
	// auth.signup.capture.token is OPTIONAL: an empty token means tmula auto-detects
	// the token from the signup response, so a signup block need not name a capture.

	steps, err := buildSignupSteps(a.Signup.Flow)
	if err != nil {
		return domain.CredentialPool{}, err
	}
	flow := &domain.SignupFlow{
		Steps:   steps,
		Start:   domain.ID(a.Signup.Start),
		Capture: domain.SignupCapture{Token: a.Signup.Capture.Token, Subject: a.Signup.Capture.Subject},
	}
	if len(a.Signup.Teardown) > 0 {
		teardown, err := buildSignupSteps(a.Signup.Teardown)
		if err != nil {
			return domain.CredentialPool{}, err
		}
		flow.Teardown = teardown
		flow.TeardownStart = domain.ID(a.Signup.TeardownStart)
	}
	return domain.CredentialPool{
		ID:           "cli-pool",
		Strategy:     domain.CredBootstrapSignup,
		SignupFlow:   flow,
		KeepAccounts: a.KeepAccounts,
	}, nil
}

// buildSignupSteps translates authored flow steps ("METHOD /path" shorthand) into
// the transport-free domain.SignupStep shape (split method/path) the orchestrator
// compiles. A pure-state step (no request) is not meaningful in a signup/teardown
// journey, so an empty request is an error.
func buildSignupSteps(flow []Step) ([]domain.SignupStep, error) {
	steps := make([]domain.SignupStep, 0, len(flow))
	for _, st := range flow {
		if st.ID == "" {
			return nil, fmt.Errorf("scenariofile: every signup/teardown step needs an id")
		}
		if st.Request == "" {
			return nil, fmt.Errorf("scenariofile: signup/teardown step %q needs a request", st.ID)
		}
		method, path, err := parseRequest(st.Request)
		if err != nil {
			return nil, fmt.Errorf("scenariofile: step %q: %w", st.ID, err)
		}
		steps = append(steps, domain.SignupStep{
			ID:        domain.ID(st.ID),
			Method:    method,
			Path:      path,
			Headers:   st.Headers,
			Body:      st.Body,
			Extract:   st.Extract,
			DependsOn: domain.ID(st.DependsOn),
			Weight:    st.Weight,
		})
	}
	return steps, nil
}

// credentialSourceFor builds the auth.CredentialSource an AuthSource block names.
// A file source is rooted at dir (the scenario file's directory) so a relative
// path resolves there and is confined to it; an empty dir falls back to the
// process working directory.
func credentialSourceFor(a AuthSource, dir string) (auth.CredentialSource, error) {
	hasFile, hasEnv := a.File != "", a.Env != ""
	if hasFile == hasEnv {
		return nil, fmt.Errorf("scenariofile: auth.source needs exactly one of file or env")
	}
	format := auth.Format(a.Format)
	if hasEnv {
		return auth.EnvSource{Var: a.Env, Format: format}, nil
	}
	root := dir
	if root == "" {
		root = "."
	}
	return auth.FileSource{Root: root, Path: a.File, Format: format}, nil
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
			Extract:         st.Extract,
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
