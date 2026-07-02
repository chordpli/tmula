// Package runspec holds RunSpec, the self-contained experiment definition the
// control plane runs. It lives in its own leaf package (depending only on
// domain, load, auth and obs) so config producers like scenariofile can name
// the type without importing the whole api control plane.
package runspec

import (
	"context"
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

	// SuggestedSignup is the declarative signup flow the importer derived from a
	// register/signup operation, offered to the UI as a "create test accounts"
	// suggestion that is INDEPENDENT of the primary CredentialPool (a login pool can
	// be the primary auth while a signup is suggested separately). It is advisory
	// only: the run path never reads it (a bootstrap-signup run carries its flow on
	// the pool's SignupFlow instead), so it is not validated and never affects a run.
	// Nil when the imported spec named no register operation. It carries no secret —
	// the token is captured at run time from the live signup response.
	SuggestedSignup *domain.SignupFlow `json:"suggestedSignup,omitempty"`

	// AuthAdvisories are import-time hints about the document's auth the importer
	// could not act on (managed-IdP mint footgun, openIdConnect discovery pointer),
	// surfaced by the /import response so the UI can warn before the operator picks
	// a strategy that cannot work. Advisory only: the run path never reads them.
	AuthAdvisories []domain.AuthAdvisory `json:"authAdvisories,omitempty"`

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
	// RefreshRequest / RefreshBody are an OPTIONAL explicit refresh-grant override the
	// orchestrator builds the mid-run refresh transport from. When RefreshBody is set it
	// WINS over the auto-derivation from the login's form grant — so even a login that
	// auto-derive cannot rewrite (a JSON-body login, or a form login with no grant_type)
	// still gets a real grant_type=refresh_token exchange. RefreshRequest is the
	// "METHOD /path" the refresh POSTs to; it is OPTIONAL and defaults to the login token
	// endpoint when empty. Both empty is the unchanged auto-derive / re-login behavior.
	// Neither carries a secret — the refresh token is captured at run time from the live
	// login response, consistent with how the access token never round-trips.
	RefreshRequest string `json:"refreshRequest,omitempty"`
	RefreshBody    string `json:"refreshBody,omitempty"`
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
	// The explicit refresh override carries no secret (the refresh token is captured at
	// run time), so only its request line is shape-checked, and ONLY when set: an empty
	// RefreshRequest is valid — it either defers to the login token endpoint (body-only
	// override) or, with an empty RefreshBody too, keeps the auto-derive / re-login path.
	if strings.TrimSpace(f.RefreshRequest) != "" {
		if err := validateMethodPath(f.RefreshRequest); err != nil {
			return fmt.Errorf("login flow: refresh request: %w", err)
		}
	}
	return nil
}

// validateMethodPath shape-checks a "METHOD /path" request line: exactly two
// whitespace-separated fields, with the path rooted and free of a scheme/authority/
// control characters (reusing validateTemplatePath). It is the same shape the
// scenariofile "METHOD /path" shorthand parses into, checked here so a malformed
// refresh-override request line is rejected up front rather than at compile time.
func validateMethodPath(req string) error {
	fields := strings.Fields(req)
	if len(fields) != 2 {
		return fmt.Errorf("must be \"METHOD /path\"")
	}
	return validateTemplatePath(fields[1])
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
// conflict; on top of that the run path applies the D1 distributed-auth split.
//
// The reject/allow-with-workers decision is CENTRALIZED in authmatrix.go (the D1
// split contract, the per-strategy WorkerRejection table, and the
// reproduce-fidelity rationale live there). This function is the small amount of
// spec-shaped glue around that table: the source carve-out and the two in-process
// requirement gates (bootstrap's SignupFlow + teardown-or-keep, login's flow).
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
	// In-process requirement gates run BEFORE the worker rejection so a missing
	// signupFlow / loginFlow surfaces its specific reason even for a workers spec.
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
	// No distributable source and workers requested: reject per the strategy's
	// centralized rule (authmatrix.go). This rejection is load-bearing for reproduce
	// fidelity — change the table there, in lockstep with the characterization test.
	if hasWorkers {
		return fmt.Errorf("%s", workerRejectionFor(r.CredentialPool.Strategy))
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
	// The mint strategy self-issues a JWT per virtual user; it needs NO flow (no
	// login/signup/refresh transport), so it builds RIGHT HERE at the leaf. The signing
	// key is resolved IN-PROCESS from the pool's non-secret key reference (env var read
	// verbatim, or a file confined under the process working directory) and handed to
	// the provider — only the reference ever crossed the wire, never the key (AD-011).
	// Acquire is then a pure, deterministic function of the user index, so the open,
	// closed and reproduce paths all build the same provider with no extra wiring.
	if r.CredentialPool.Strategy == domain.CredMint {
		if r.CredentialPool.Mint == nil {
			return nil, fmt.Errorf("api: a mint credential pool has no mint spec")
		}
		key, err := auth.ResolveMintKey(context.Background(), *r.CredentialPool.Mint, "")
		if err != nil {
			return nil, fmt.Errorf("api: resolve mint signing key: %w", err)
		}
		return auth.NewProvider(*r.CredentialPool, auth.ProviderDeps{MintKey: key})
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
