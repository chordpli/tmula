// Package runspec holds RunSpec, the self-contained experiment definition the
// control plane runs. It lives in its own leaf package (depending only on
// domain, load, auth and obs) so config producers like scenariofile can name
// the type without importing the whole api control plane.
package runspec

import (
	"fmt"
	"strings"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/obs"
)

// RunSpec is a self-contained experiment definition: everything needed to run.
type RunSpec struct {
	Experiment domain.Experiment                `json:"experiment"`
	TargetEnv  domain.TargetEnv                 `json:"targetEnv"`
	Graph      domain.ScenarioGraph             `json:"graph"`
	Templates  map[domain.ID]domain.APITemplate `json:"templates"`
	Start      domain.ID                        `json:"start"`
	MaxSteps   int                              `json:"maxSteps"`
	Users      []load.VirtualUser               `json:"users"`
	// UserCount sizes the closed-model virtual-user pool when Users is empty: the
	// server synthesizes u0..u{UserCount-1} at run (and shard) time instead of the
	// client shipping one object per user, so a huge closed run fits in a small
	// request body rather than overflowing the create-request size limit. An
	// explicit Users list always wins; the open model ignores it (it generates its
	// own sessions from the arrival rate).
	UserCount int   `json:"userCount,omitempty"`
	Seed      int64 `json:"seed"`
	// Workers lists gRPC worker addresses to distribute the run across. When
	// empty the run executes locally in-process; when set, the control plane
	// dials each worker, fans the virtual users out across them, and aggregates
	// their streamed results identically to the local path.
	Workers []string `json:"workers,omitempty"`

	// AggregateWorkers makes a distributed run aggregate on the workers: each
	// worker folds its whole shard into a compact summary and the master merges
	// those, instead of streaming every request. It trades per-endpoint and
	// run-length finding fidelity for bounded network + memory at huge request
	// volumes. Ignored unless Workers is set.
	AggregateWorkers bool `json:"aggregateWorkers,omitempty"`

	// Workload selects the user-generation model. Nil or a closed model runs a
	// fixed set of users (the default); an open model generates sessions at an
	// arrival rate over time so concurrency emerges organically.
	Workload *domain.WorkloadModel `json:"workload,omitempty"`

	// Segments is the persona mix for an open run: weighted behavioral profiles
	// (entry node, step bound, think time) the arrivals are drawn from. It only
	// applies to the open model; the closed path ignores it.
	Segments []domain.Segment `json:"segments,omitempty"`

	// Trace opts a small run (<= traceMaxUsers) into live per-request event
	// streaming for the traffic graph (GET /runs/{id}/trace). Larger runs ignore
	// it — it is an inspect view, not a millions-scale feature.
	Trace bool `json:"trace,omitempty"`

	// Metrics, when set, opts the run into server-side metric correlation: after
	// the run finishes the named PromQL queries are fetched from Prometheus over
	// the run's window and attached to the report beside the client-side stats.
	// It is observability only — a fetch failure becomes a note on the report,
	// never a run failure. The Prometheus host must be in the target allowlist,
	// like every other host the engine reaches.
	Metrics *domain.MetricsSource `json:"metrics,omitempty"`

	// Findings, when set, tunes how the run's observations are classified into
	// findings: the error-rate threshold, the (otherwise disabled) p95 latency
	// gate, and the consecutive-failure streak that flags availability. A nil
	// block — and any zero field within it — keeps the long-standing defaults
	// (see obs.DefaultClassifyConfig), so existing specs classify exactly as
	// before.
	Findings *obs.FindingConfig `json:"findings,omitempty"`

	// CredentialPool, when set, authenticates the run: each virtual user (closed)
	// or session (open) is assigned a credential by index from the pool, so the
	// simulated traffic carries real auth material instead of running anonymously.
	// The pool strategy must be "pool" (pre-supplied entries) on this path;
	// bootstrap-signup is a documented follow-up (it needs a signup transport this
	// path does not yet wire). The credential secret carries json:"-" (domain), so
	// a persisted or streamed spec never leaks it. Nil leaves the run
	// unauthenticated, exactly as before.
	CredentialPool *domain.CredentialPool `json:"credentialPool,omitempty"`

	// id is internal run-bookkeeping: the run identifier the control plane assigns
	// after creation (see SetID). It is never serialized and never read back out
	// of the spec by the run path.
	id domain.ID
}

// ID returns the run identifier the control plane assigned to this spec.
func (r RunSpec) ID() domain.ID { return r.id }

// SetID records the run identifier on the spec. It is internal run-bookkeeping,
// set by the control plane after the spec is created.
func (r *RunSpec) SetID(id domain.ID) { r.id = id }

// Validate checks the spec is runnable.
func (r RunSpec) Validate() error {
	if err := r.TargetEnv.Validate(); err != nil {
		return err
	}
	if err := r.Graph.Validate(); err != nil {
		return err
	}
	if err := r.Experiment.Validate(); err != nil {
		return err
	}
	// Validate every template's path so a static authority/scheme/CRLF cannot be
	// smuggled into the request URL. (A variable that renders into the path is
	// additionally caught at request time by the guard's allowlist check.)
	for id, t := range r.Templates {
		if t.Method == "" {
			return fmt.Errorf("api: template %q: method is required", id)
		}
		if err := validateTemplatePath(t.Path); err != nil {
			return fmt.Errorf("api: template %q path %q: %w", id, t.Path, err)
		}
	}
	if r.Start == "" {
		return fmt.Errorf("api: start node is required")
	}
	if r.Workload != nil {
		if err := r.Workload.Validate(); err != nil {
			return err
		}
	}
	// The open model generates its own sessions from the arrival rate, so it
	// needs no user list; every other path needs at least one user — supplied
	// either as an explicit pool or as a positive UserCount the server expands.
	if !r.IsOpen() && r.PoolSize() <= 0 {
		return fmt.Errorf("api: at least one virtual user is required")
	}
	// The open model runs in-process only; refuse worker fields rather than
	// silently dropping them and running locally.
	if r.IsOpen() && (len(r.Workers) > 0 || r.AggregateWorkers) {
		return fmt.Errorf("api: distributed workers are not supported with the open workload model")
	}
	if len(r.Segments) > 0 {
		if !r.IsOpen() {
			return fmt.Errorf("api: segments (personas) apply only to the open workload model")
		}
		if err := domain.ValidateSegments(r.Segments); err != nil {
			return err
		}
		// A segment's entry node must exist in the graph, else its sessions would
		// fail to walk at runtime; reject up front with a clear message.
		nodes := make(map[domain.ID]bool, len(r.Graph.Nodes))
		for _, n := range r.Graph.Nodes {
			nodes[n.ID] = true
		}
		for _, seg := range r.Segments {
			if seg.Start != "" && !nodes[seg.Start] {
				return fmt.Errorf("api: segment %q start node %q is not in the graph", seg.Name, seg.Start)
			}
		}
	}
	if err := r.validateCredentialPool(); err != nil {
		return err
	}
	if err := r.Findings.Validate(); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	if r.Metrics != nil {
		if err := r.Metrics.Validate(); err != nil {
			return fmt.Errorf("api: %w", err)
		}
	}
	return nil
}

// validateCredentialPool checks an optional credential pool is usable on this
// path. A nil pool is fine (the run is unauthenticated). The domain validation
// rejects an unknown strategy and an empty "pool" strategy; on top of that, the
// run path supports only the pre-supplied "pool" strategy today, so a
// bootstrap-signup request fails loudly rather than silently running
// unauthenticated, and a credential pool combined with distributed workers is
// refused because the worker fan-out synthesizes its own (unauthenticated) users.
func (r RunSpec) validateCredentialPool() error {
	if r.CredentialPool == nil {
		return nil
	}
	if err := r.CredentialPool.Validate(); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	if r.CredentialPool.Strategy == domain.CredBootstrapSignup {
		// Follow-up: the bootstrap provider exists but needs a signup transport this
		// run path does not yet wire. Refuse rather than run unauthenticated.
		return fmt.Errorf("api: credential strategy %q is not yet supported via this run path (follow-up); use the %q strategy with pre-supplied entries", domain.CredBootstrapSignup, domain.CredPool)
	}
	if len(r.Workers) > 0 || r.AggregateWorkers {
		return fmt.Errorf("api: a credential pool is not yet supported with distributed workers (the worker fan-out synthesizes its own users)")
	}
	return nil
}

// CredentialProvider builds the auth provider for a run from its credential pool,
// or returns (nil, nil) when the run is unauthenticated. Validate has already
// confirmed the pool is a usable "pool" strategy, so no signup function is needed.
func (r RunSpec) CredentialProvider() (auth.Provider, error) {
	if r.CredentialPool == nil {
		return nil, nil
	}
	return auth.NewProvider(*r.CredentialPool, nil)
}

// IsOpen reports whether the spec uses the open (arrival-rate) workload model.
func (r RunSpec) IsOpen() bool {
	return r.Workload != nil && r.Workload.Kind == domain.WorkloadOpen
}

// PoolSize is the closed-model virtual-user count: the explicit Users length when
// the client shipped a pool, otherwise the UserCount it asked the server to
// synthesize. It sizes both the local pool and the distributed fan-out, so the two
// paths agree on how many users a count-only run drives.
func (r RunSpec) PoolSize() int {
	if len(r.Users) > 0 {
		return len(r.Users)
	}
	return r.UserCount
}

// ClosedUsers returns the virtual-user pool for a closed run. It returns the
// explicit Users when the client sent them; otherwise it synthesizes a stable
// u0..u{UserCount-1} pool so a large run need not ship one object per user. Callers
// reach it only after Validate has ensured PoolSize > 0, so the count is positive.
func (r RunSpec) ClosedUsers() []load.VirtualUser {
	if len(r.Users) > 0 {
		return r.Users
	}
	users := make([]load.VirtualUser, r.UserCount)
	for i := range users {
		users[i] = load.VirtualUser{ID: fmt.Sprintf("u%d", i)}
	}
	return users
}

// validateTemplatePath rejects a template path that could redirect a request off
// the target host: it must be a rooted path (start with a single "/"), carry no
// scheme or authority, and contain no control characters. A "//" prefix is
// refused because it is a protocol-relative authority.
func validateTemplatePath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("must be a rooted path starting with /")
	}
	if strings.HasPrefix(path, "//") {
		return fmt.Errorf("must not start with // (protocol-relative authority)")
	}
	if strings.Contains(path, "://") {
		return fmt.Errorf("must not contain a scheme")
	}
	if strings.ContainsAny(path, "\r\n\t") {
		return fmt.Errorf("must not contain control characters")
	}
	return nil
}
