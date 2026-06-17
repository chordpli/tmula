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

	// LoginFlow carries the standalone login flow a CredLogin pool mints tokens
	// from: its own graph, templates, start node and the response captures that
	// become the token (and subject). It is a sibling of the main scenario graph —
	// never a node in it — so the simulated traffic never observes the login. It is
	// required when the pool's strategy is "login" and ignored otherwise. The
	// orchestrator compiles it (above the load runner) into the login transport;
	// runspec stays a leaf and only carries the declarative domain types.
	LoginFlow *LoginFlowSpec `json:"loginFlow,omitempty"`

	// id is internal run-bookkeeping: the run identifier the control plane assigns
	// after creation (see SetID). It is never serialized and never read back out
	// of the spec by the run path.
	id domain.ID
}

// LoginFlowSpec is the declarative login flow a CredLogin pool walks to mint a
// token. It carries only domain types (graph, templates, capture variable names),
// so runspec stays a leaf the orchestrator compiles into a runnable login
// transport. It holds no secret — the token is captured at run time from the live
// login response and never round-trips through a spec.
type LoginFlowSpec struct {
	Graph     domain.ScenarioGraph             `json:"graph"`
	Templates map[domain.ID]domain.APITemplate `json:"templates"`
	Start     domain.ID                        `json:"start"`
	MaxSteps  int                              `json:"maxSteps,omitempty"`
	// TokenVar names the captured variable that becomes the credential's secret.
	// Optional: an empty TokenVar means the runner auto-detects the token from the
	// login response (see load.DetectCredential). SubjectVar, when set, names the
	// captured variable that becomes the non-sensitive subject.
	TokenVar   string `json:"tokenVar,omitempty"`
	SubjectVar string `json:"subjectVar,omitempty"`
}

// Validate checks the login flow is well-formed: a non-empty graph and a start node
// present in it. The token capture variable is optional — an empty TokenVar means
// the runner auto-detects the token from the login response — so a flow without an
// explicit capture is valid.
func (f LoginFlowSpec) Validate() error {
	if err := f.Graph.Validate(); err != nil {
		return fmt.Errorf("login flow: %w", err)
	}
	if f.Start == "" {
		return fmt.Errorf("login flow: a start node is required")
	}
	known := false
	for _, n := range f.Graph.Nodes {
		if n.ID == f.Start {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("login flow: start node %q is not in the login graph", f.Start)
	}
	return nil
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
// rejects an unknown strategy, an empty "pool" strategy, and an inline-vs-source
// conflict; on top of that the run path applies the D1 split that decides which
// authenticated runs may distribute.
//
// D1 SPLIT (the load-bearing distributed-auth contract): a distributed run
// authenticates ONLY from a shared, index-deterministic SourceRef that the worker
// resolves LOCALLY; inline secrets and bootstrap stay rejected with workers.
// Concretely:
//
//   - Source != nil && NO workers  → REJECTED. The in-process/API server must not
//     read a client-chosen path off the wire; the CLI resolves single-node sources
//     into entries at scenariofile.Expand, so a non-distributed spec must carry
//     real entries.
//   - Source (file/env) != nil && workers → ALLOWED, the distributed carve-out:
//     only the reference crosses the wire and each worker loads its own slice and
//     assigns by GLOBAL index, so every worker reconstructs the same provider
//     (PoolProvider.Acquire is a pure function of the global index). No secret is
//     serialized. shardSpecFor copies the ref into ShardSpec.CredentialSource.
//   - Inline Entries != nil && workers → REJECTED. The secrets would serialize
//     into the wire spec.
//   - Bootstrap-signup && NO workers → ALLOWED when it carries a SignupFlow and
//     either a teardown journey OR --keep-accounts (the gating-safety rule). The
//     orchestrator compiles the SignupFlow, prewarms one account per virtual user,
//     and defers teardown. A bootstrap pool with no SignupFlow, or no teardown and
//     no keep-accounts, is REJECTED above.
//   - Bootstrap-signup && workers → REJECTED. A bootstrap pool mints real accounts
//     and has no shared reference to fan out; P4 keeps this rejected (distributed
//     bootstrap is a follow-up). (Domain Validate already forbids a Source on a
//     bootstrap pool.)
//   - Login && workers → REJECTED. A minted login token is a json:"-" secret the
//     worker fan-out cannot resolve. (Domain Validate forbids a Source on a login
//     pool, so login never reaches the carve-out.)
//
// LOAD-BEARING FOR REPRODUCE FIDELITY: every distributed authenticated run is
// either rejected here or carries a source the workers (and reproduce) resolve by
// the SAME pure Acquire(global index). reproduce.go's sessionUser relies on this:
// a distributed-auth finding replays under the same principal the shard ran as
// because both rebuild the source-backed provider and key it by the global index.
// If this split is ever changed, sessionUser must be updated in lockstep (D4: PR3
// and PR4 land together). See also: CredentialProvider, the sessionUser function
// in reproduce.go, and shardSpecFor in orchestrator.go.
func (r RunSpec) validateCredentialPool() error {
	if r.CredentialPool == nil {
		return nil
	}
	if err := r.CredentialPool.Validate(); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	hasWorkers := len(r.Workers) > 0 || r.AggregateWorkers
	if r.CredentialPool.Source != nil {
		// Distributed carve-out: a source pool fanned out across workers ships only
		// a reference; each worker resolves it locally and assigns by global index.
		// Domain Validate guarantees a Source only ever rides a CredPool (login and
		// bootstrap reject it), and the ref's shape (exactly one of file/env, known
		// format) is already validated, so a present source here is always a usable
		// distributed pool reference.
		if hasWorkers {
			return nil
		}
		// Source without workers: the single-node path must arrive pre-resolved.
		// The CLI resolves auth.source into entries at scenariofile.Expand time, so
		// the server never reads a client-chosen path off the wire.
		return fmt.Errorf("api: credential source must be resolved before running (the CLI resolves it at expand time; a distributed run with workers ships the reference instead)")
	}
	if r.CredentialPool.Strategy == domain.CredBootstrapSignup {
		// A bootstrap-signup pool provisions one real account per virtual user up
		// front (the orchestrator compiles its SignupFlow and prewarms it). Two gates
		// apply on the IN-PROCESS path:
		//
		//   1. It must carry a declarative SignupFlow — the legacy bare BootstrapFlowID
		//      is not runnable (nothing to compile and walk).
		//   2. GATING SAFETY: it must either declare a teardown journey OR opt out with
		//      KeepAccounts. A bootstrap run with no teardown and no keep-accounts is
		//      refused, so a load test never strands thousands of real accounts.
		//
		// (bootstrap + workers is rejected below, with the other inline-secret pools —
		// distributed bootstrap is a follow-up that has no shared reference to fan out.)
		if r.CredentialPool.SignupFlow == nil {
			return fmt.Errorf("api: the %q strategy needs a signupFlow describing how to provision an account", domain.CredBootstrapSignup)
		}
		if !r.CredentialPool.SignupFlow.HasTeardown() && !r.CredentialPool.KeepAccounts {
			return fmt.Errorf("api: the %q strategy provisions real accounts and must deprovision them: declare a teardown flow, or pass --keep-accounts to leave them in place", domain.CredBootstrapSignup)
		}
	}
	if r.CredentialPool.Strategy == domain.CredLogin {
		// A login pool mints tokens by walking a standalone login flow, so the spec
		// must carry that flow (the orchestrator compiles it into the login
		// transport). Reject a login pool with no — or a malformed — login flow.
		if r.LoginFlow == nil {
			return fmt.Errorf("api: the %q strategy needs a loginFlow describing how to mint a token", domain.CredLogin)
		}
		if err := r.LoginFlow.Validate(); err != nil {
			return fmt.Errorf("api: %w", err)
		}
	}
	if hasWorkers {
		// Inline-secret carry (entries pool or a minted login token) cannot fan out:
		// the secret would serialize into the wire spec / a json:"-" secret the
		// worker cannot resolve. A bootstrap pool mints real accounts and has no
		// shared reference to fan out either — distributed bootstrap is a follow-up,
		// kept rejected here (P3 set this, P4 keeps it). Only a source-backed pool
		// reaches workers (handled above). This rejection is load-bearing for
		// reproduce fidelity — see the doc comment before relaxing it.
		if r.CredentialPool.Strategy == domain.CredBootstrapSignup {
			return fmt.Errorf("api: the %q strategy is not supported with distributed workers (a bootstrap pool provisions per-node accounts and has no shared reference to fan out; distributed bootstrap is a follow-up)", domain.CredBootstrapSignup)
		}
		return fmt.Errorf("api: an inline credential pool is not supported with distributed workers (only a reference-only source pool fans out; ship a credential source instead)")
	}
	return nil
}

// CredentialProvider builds the auth provider for an IN-PROCESS run from its
// credential pool, or returns (nil, nil) when the run is unauthenticated. Validate
// has already confirmed the pool is a usable "pool" strategy with resolved entries
// (an unresolved source is rejected without workers, and the distributed path
// below never calls this).
//
// IN-PROCESS ONLY: the distributed path does NOT call CredentialProvider — it
// copies the pool's reference-only source into the shard spec (shardSpecFor) and
// each worker resolves it locally. A source pool therefore only ever reaches a
// distributed run, so it never arrives here (Validate rejects source + no workers
// on the in-process path). The reproduce path rebuilds a distributed-auth run's
// provider directly from the source (sessionUser/sourceProviderFor in
// reproduce.go), keyed by the same global index the shards used. See
// validateCredentialPool for the full D1 split invariant.
func (r RunSpec) CredentialProvider() (auth.Provider, error) {
	if r.CredentialPool == nil {
		return nil, nil
	}
	// The login strategy needs a token transport (the compiled login flow) that this
	// leaf package cannot build — runspec must not import load/api. The api
	// orchestrator builds the login provider itself (see api.providerFor / the login
	// transport) and never reaches here for a login pool, so a login pool arriving
	// here is a wiring bug, not a silent unauthenticated run.
	if r.CredentialPool.Strategy == domain.CredLogin {
		return nil, fmt.Errorf("api: a login credential pool's provider is built by the orchestrator, not runspec (login transport lives above this leaf)")
	}
	// Bootstrap-signup likewise needs a signup transport (the compiled SignupFlow)
	// that this leaf cannot build — the orchestrator builds the provider (see
	// api.bootstrapAuthFor) and the run path never reaches here for a bootstrap pool,
	// so a bootstrap pool arriving here is a wiring bug, not a silent unauthenticated
	// run.
	if r.CredentialPool.Strategy == domain.CredBootstrapSignup {
		return nil, fmt.Errorf("api: a bootstrap-signup credential pool's provider is built by the orchestrator, not runspec (signup transport lives above this leaf)")
	}
	// The remaining strategies (pool today) need no signup/token function — an empty
	// ProviderDeps.
	return auth.NewProvider(*r.CredentialPool, auth.ProviderDeps{})
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
