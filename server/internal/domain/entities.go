package domain

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

// ID is an opaque identifier for domain entities.
type ID string

// --- Target environment -----------------------------------------------------

// RateCap is a hard ceiling enforced by the safety guard before any send.
type RateCap struct {
	MaxRPS         int `json:"maxRps"`
	MaxConcurrency int `json:"maxConcurrency"`
}

// TargetEnv is a system-under-test endpoint plus its safety constraints.
type TargetEnv struct {
	ID        ID       `json:"id"`
	BaseURL   string   `json:"baseUrl"`
	Allowlist []string `json:"allowlist"` // host patterns permitted as targets
	RateCap   RateCap  `json:"rateCap"`
	EnvClass  EnvClass `json:"envClass"`
}

// Validate enforces the invariants required before traffic may be sent.
func (t TargetEnv) Validate() error {
	if t.BaseURL == "" {
		return errors.New("target env: baseUrl is required")
	}
	if !t.EnvClass.Valid() {
		return fmt.Errorf("target env: invalid envClass %q", t.EnvClass)
	}
	if len(t.Allowlist) == 0 {
		return errors.New("target env: allowlist must not be empty (no unrestricted targets)")
	}
	if t.RateCap.MaxRPS <= 0 || t.RateCap.MaxConcurrency <= 0 {
		return errors.New("target env: rateCap maxRps and maxConcurrency must be > 0")
	}
	return nil
}

// --- API template -----------------------------------------------------------

// APITemplate describes one callable endpoint and its request shape.
type APITemplate struct {
	ID              ID                `json:"id"`
	Protocol        Protocol          `json:"protocol"`
	Method          string            `json:"method"`
	Path            string            `json:"path"`
	Headers         map[string]string `json:"headers,omitempty"`
	PayloadTemplate string            `json:"payloadTemplate,omitempty"`
	Extract         map[string]string `json:"extract,omitempty"`
}

// Validate checks the template is callable.
func (a APITemplate) Validate() error {
	if !a.Protocol.Valid() {
		return fmt.Errorf("api template %q: invalid protocol %q", a.ID, a.Protocol)
	}
	if a.Method == "" || a.Path == "" {
		return fmt.Errorf("api template %q: method and path are required", a.ID)
	}
	return nil
}

// --- Scenario graph ---------------------------------------------------------

// Node is a state in the behavior graph, bound to an API template. A node
// without a template is terminal (done / exit) and is serialized without the
// field, matching the documented graph examples.
type Node struct {
	ID            ID `json:"id"`
	APITemplateID ID `json:"apiTemplateId,omitempty"`
}

// Edge is a possible transition between nodes. When Dependency is true the
// target requires this predecessor: it is a hard precondition that random
// deviation must never skip.
type Edge struct {
	From       ID      `json:"from"`
	To         ID      `json:"to"`
	Weight     float64 `json:"weight"`
	Dependency bool    `json:"dependency,omitempty"`
}

// ScenarioGraph is the explicit behavior frame virtual users traverse.
type ScenarioGraph struct {
	ID    ID     `json:"id"`
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Validate performs structural checks that do not require traversal.
// Full reachability/cycle validation lives in the graph format parser.
func (g ScenarioGraph) Validate() error {
	if len(g.Nodes) == 0 {
		return errors.New("scenario graph: at least one node is required")
	}
	known := make(map[ID]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.ID == "" {
			return errors.New("scenario graph: node id must not be empty")
		}
		if known[n.ID] {
			return fmt.Errorf("scenario graph: duplicate node id %q", n.ID)
		}
		known[n.ID] = true
	}
	for _, e := range g.Edges {
		if !known[e.From] || !known[e.To] {
			return fmt.Errorf("scenario graph: edge %q->%q references unknown node", e.From, e.To)
		}
		// Use a positive predicate so NaN (which fails every comparison) is
		// rejected rather than silently passing as "not negative"; also reject
		// +Inf, which would satisfy ">= 0" yet poison a weighted pick.
		if !(e.Weight >= 0) || math.IsInf(e.Weight, 1) {
			return fmt.Errorf("scenario graph: edge %q->%q has invalid weight %v", e.From, e.To, e.Weight)
		}
	}
	return nil
}

// --- Credentials ------------------------------------------------------------

// Credential is one principal's auth material. The secret is never serialized
// (masking at rest, AD-011); only a non-sensitive subject is persisted.
type Credential struct {
	Subject string `json:"subject"`
	Secret  string `json:"-"`
}

// String redacts the secret so a Credential cannot leak via %v/%+v in logs.
func (c Credential) String() string {
	if c.Secret == "" {
		return fmt.Sprintf("Credential{Subject:%q}", c.Subject)
	}
	return fmt.Sprintf("Credential{Subject:%q, Secret:***}", c.Subject)
}

// CredentialPool supplies credentials to virtual users.
type CredentialPool struct {
	ID              ID                 `json:"id"`
	Strategy        CredentialStrategy `json:"strategy"`
	Entries         []Credential       `json:"entries,omitempty"`
	BootstrapFlowID *ID                `json:"bootstrapFlowId,omitempty"`
}

// Validate checks the pool can actually provide credentials.
func (c CredentialPool) Validate() error {
	if !c.Strategy.Valid() {
		return fmt.Errorf("credential pool %q: invalid strategy %q", c.ID, c.Strategy)
	}
	if c.Strategy == CredPool && len(c.Entries) == 0 {
		return fmt.Errorf("credential pool %q: pool strategy needs at least one entry", c.ID)
	}
	if c.Strategy == CredBootstrapSignup && (c.BootstrapFlowID == nil || *c.BootstrapFlowID == "") {
		return fmt.Errorf("credential pool %q: bootstrap-signup needs a non-empty bootstrapFlowId", c.ID)
	}
	return nil
}

// --- Load profile -----------------------------------------------------------

// ProfileShape parameterizes a load strategy over time.
type ProfileShape struct {
	StartConcurrency int `json:"startConcurrency,omitempty"`
	PeakConcurrency  int `json:"peakConcurrency,omitempty"`
	RampSeconds      int `json:"rampSeconds,omitempty"`
	HoldSeconds      int `json:"holdSeconds,omitempty"`
}

// LoadProfile concentrates load on a target API using a strategy + shape.
type LoadProfile struct {
	ID          ID           `json:"id"`
	TargetAPIID ID           `json:"targetApiId"`
	Strategy    LoadStrategy `json:"strategy"`
	Shape       ProfileShape `json:"shape"`
}

// Validate checks the profile is well-formed.
func (l LoadProfile) Validate() error {
	if !l.Strategy.Valid() {
		return fmt.Errorf("load profile %q: invalid strategy %q", l.ID, l.Strategy)
	}
	if l.TargetAPIID == "" {
		return fmt.Errorf("load profile %q: targetApiId is required", l.ID)
	}
	s := l.Shape
	if s.StartConcurrency < 0 || s.PeakConcurrency < 0 || s.RampSeconds < 0 || s.HoldSeconds < 0 {
		return fmt.Errorf("load profile %q: shape parameters must be non-negative", l.ID)
	}
	return nil
}

// --- Experiment -------------------------------------------------------------

// ExperimentParams are the run-time knobs for an experiment.
type ExperimentParams struct {
	VirtualUserCount int                `json:"virtualUserCount"`
	DeviationRate    float64            `json:"deviationRate"` // 0..1
	AuthStrategy     CredentialStrategy `json:"authStrategy"`
}

// Experiment ties together what to run and how.
type Experiment struct {
	ID              ID               `json:"id"`
	Name            string           `json:"name"`
	TargetEnvID     ID               `json:"targetEnvId"`
	ScenarioGraphID ID               `json:"scenarioGraphId"`
	Params          ExperimentParams `json:"params"`
	CreatedAt       time.Time        `json:"createdAt"`
}

// Validate checks the experiment is runnable.
func (e Experiment) Validate() error {
	if e.Name == "" {
		return errors.New("experiment: name is required")
	}
	if e.TargetEnvID == "" || e.ScenarioGraphID == "" {
		return errors.New("experiment: targetEnvId and scenarioGraphId are required")
	}
	if e.Params.VirtualUserCount <= 0 {
		return errors.New("experiment: virtualUserCount must be > 0")
	}
	if e.Params.DeviationRate < 0 || e.Params.DeviationRate > 1 {
		return fmt.Errorf("experiment: deviationRate %v out of range [0,1]", e.Params.DeviationRate)
	}
	if !e.Params.AuthStrategy.Valid() {
		return fmt.Errorf("experiment: invalid authStrategy %q", e.Params.AuthStrategy)
	}
	return nil
}

// --- Run, metrics, findings, sharing ---------------------------------------

// RunExecution is one execution of an experiment.
type RunExecution struct {
	ID           ID         `json:"id"`
	ExperimentID ID         `json:"experimentId"`
	Mode         RunMode    `json:"mode"`
	Status       RunStatus  `json:"status"`
	StartedAt    time.Time  `json:"startedAt"`
	EndedAt      *time.Time `json:"endedAt,omitempty"`
	KillReason   string     `json:"killReason,omitempty"`
	// Workers is the number of remote workers a distributed run fanned out to
	// (0 for a local run). It is persisted on the run so a report rebuilt from the
	// store (after eviction or a restart) can show the same topology the live one
	// did, without recomputing it from a spec that may no longer be retained.
	Workers int `json:"workers,omitempty"`
}

// MetricSample is one observed client-side data point.
type MetricSample struct {
	RunID      ID        `json:"runId"`
	TS         time.Time `json:"ts"`
	APIID      ID        `json:"apiId"`
	StatusCode int       `json:"statusCode"`
	LatencyMs  float64   `json:"latencyMs"`
	ErrorClass string    `json:"errorClass,omitempty"`
	WorkerID   string    `json:"workerId,omitempty"`
}

// Finding is a classified issue surfaced by a run.
type Finding struct {
	RunID       ID              `json:"runId"`
	Category    FindingCategory `json:"category"`
	Severity    Severity        `json:"severity"`
	EvidenceRef string          `json:"evidenceRef,omitempty"`
	FirstSeen   time.Time       `json:"firstSeen"`
	Description string          `json:"description"`
}

// MetricQuery names one PromQL expression to correlate with a run.
type MetricQuery struct {
	// Name labels the series in the report (e.g. "db connections").
	Name string `json:"name"`
	// Query is the PromQL expression evaluated over the run's window.
	Query string `json:"query"`
}

// MetricsSource opts a run into server-side metric correlation: after the run
// finishes, each query is fetched from Prometheus over the run's time window
// and the series are placed beside the client-side stats in the report. It is
// observability only — fetch failures never fail the run.
type MetricsSource struct {
	PrometheusURL string        `json:"prometheusUrl"`
	Queries       []MetricQuery `json:"queries"`
}

// Validate checks the source is fetchable: an absolute http(s) URL and at
// least one named, non-empty query with no duplicate names.
func (m MetricsSource) Validate() error {
	u, err := url.Parse(m.PrometheusURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("metrics: prometheusUrl %q must be an absolute http(s) URL", m.PrometheusURL)
	}
	if len(m.Queries) == 0 {
		return fmt.Errorf("metrics: at least one query is required")
	}
	seen := make(map[string]bool, len(m.Queries))
	for i, q := range m.Queries {
		if strings.TrimSpace(q.Name) == "" {
			return fmt.Errorf("metrics: query %d: name is required", i)
		}
		if strings.TrimSpace(q.Query) == "" {
			return fmt.Errorf("metrics: query %q: query is required", q.Name)
		}
		if seen[q.Name] {
			return fmt.Errorf("metrics: duplicate query name %q", q.Name)
		}
		seen[q.Name] = true
	}
	return nil
}

// MetricPoint is one sample of a fetched series: a unix-millisecond timestamp
// and its value.
type MetricPoint struct {
	TS int64   `json:"ts"`
	V  float64 `json:"v"`
}

// MetricSeries is one server-side time series fetched for a run, rendered
// beside the run's own timeline in the report.
type MetricSeries struct {
	Name   string        `json:"name"`
	Points []MetricPoint `json:"points"`
}

// ReportShare grants read-only access to a run report via an opaque token.
type ReportShare struct {
	ID        ID         `json:"id"`
	RunID     ID         `json:"runId"`
	Token     string     `json:"token"`
	Scope     AccessRole `json:"scope"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// Expired reports whether the share is past its expiry at time now.
func (r ReportShare) Expired(now time.Time) bool {
	return r.ExpiresAt != nil && now.After(*r.ExpiresAt)
}
