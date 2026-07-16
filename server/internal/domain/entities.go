package domain

import (
	"encoding/json"
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

// Credential is one principal's auth material. Secrets are never serialized
// (masking at rest, AD-011); only a non-sensitive subject is persisted. Refresh
// and ExpiresIn are the OAuth2 grant data a later real grant_type=refresh_token
// transport reads: Refresh is the refresh token (a secret, json:"-", redacted by
// String like Secret) and ExpiresIn is the access token's lifetime straight from
// the login response's expires_in (seconds). ExpiresIn is runtime-only (json:"-")
// so the "only Subject persists" contract is unchanged; both stay zero when the
// login response carries neither field, leaving the credential identical to a
// pre-refresh mint.
type Credential struct {
	Subject   string        `json:"subject"`
	Secret    string        `json:"-"`
	Refresh   string        `json:"-"`
	ExpiresIn time.Duration `json:"-"`
}

// UnmarshalJSON reads the INBOUND wire shape the console posts for an inline
// credential entry: {"subject","token"} — token lands in Secret. Without this,
// json:"-" silently DROPPED the posted token and a web-authored pool ran with
// empty secrets. The tag stays "-" so Marshal never emits a secret (masking at
// rest, AD-011): the token is write-only across the wire — a stored/shared spec
// round-trips with only the subject. Refresh and ExpiresIn are run-time state,
// never wire-supplied.
func (c *Credential) UnmarshalJSON(b []byte) error {
	var w struct {
		Subject string `json:"subject"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	c.Subject = w.Subject
	c.Secret = w.Token
	return nil
}

// String redacts BOTH secrets (Secret and Refresh) so a Credential cannot leak a
// token via %v/%+v in logs; the non-sensitive subject and the (non-secret) expiry
// are shown to keep the value debuggable.
func (c Credential) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Credential{Subject:%q", c.Subject)
	if c.Secret != "" {
		b.WriteString(", Secret:***")
	}
	if c.Refresh != "" {
		b.WriteString(", Refresh:***")
	}
	if c.ExpiresIn != 0 {
		fmt.Fprintf(&b, ", ExpiresIn:%s", c.ExpiresIn)
	}
	b.WriteByte('}')
	return b.String()
}

// AuthAdvisory is an import-time hint about the document's auth that the
// importer could not (or must not) act on itself — e.g. "mint-managed-idp"
// (the security scheme points at a managed IdP whose signing key the operator
// does not hold, so the mint strategy cannot work) or "openidconnect-discovery"
// (the scheme is openIdConnect; the discovery URL names the token_endpoint for
// the OAuth2 route). Code is a stable machine key the UI translates; Detail is
// the code-specific parameter (the IdP host, the discovery URL). Advisory only:
// the run path never reads it.
type AuthAdvisory struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// Advisory codes the importer emits. They are stable machine keys; Message renders
// each into a human-readable, actionable sentence for the CLI (and the UI can translate
// them independently).
const (
	// AdvisoryMintManagedIDP: the token issuer is a managed IdP whose signing key the
	// operator does not hold, so the mint strategy cannot forge tokens it will accept.
	AdvisoryMintManagedIDP = "mint-managed-idp"
	// AdvisoryOpenIDConnectDiscovery: the scheme is OpenID Connect; the discovery URL
	// (Detail) names the token endpoint the OAuth2/login route should point at.
	AdvisoryOpenIDConnectDiscovery = "openidconnect-discovery"
)

// Message renders the advisory into a human-readable, actionable sentence for the CLI.
// It is the single source of truth for the advisory copy so `tmula init` and `tmula run`
// (and any other surface) describe a code identically. An unknown code degrades to the
// bare code plus its detail rather than an empty string, so a newly-added advisory is
// never invisible.
func (a AuthAdvisory) Message() string {
	switch a.Code {
	case AdvisoryMintManagedIDP:
		host := a.Detail
		if host == "" {
			host = "a managed identity provider"
		}
		return fmt.Sprintf("the token issuer is a managed IdP (%s) whose signing key you do not hold — the mint strategy cannot forge tokens it will accept; use the login strategy against the IdP instead", host)
	case AdvisoryOpenIDConnectDiscovery:
		url := a.Detail
		if url == "" {
			url = "the issuer's discovery document"
		}
		return fmt.Sprintf("the security scheme is OpenID Connect; its discovery document (%s) names the token endpoint — point auth.login at that token URL (POST /auth/discover can fetch it for you)", url)
	default:
		if a.Detail != "" {
			return fmt.Sprintf("%s (%s)", a.Code, a.Detail)
		}
		return a.Code
	}
}

// CredentialSourceRef is a non-secret pointer to an external credential pool: a
// file path (relative to the scenario document) or an environment variable, plus
// the format its body is encoded in. It carries no secret field by design, so a
// pool that references an external source can serialize (and cross the wire) with
// only the reference — never the tokens it resolves to (AD-011).
type CredentialSourceRef struct {
	File   string `json:"file,omitempty"`
	Env    string `json:"env,omitempty"`
	Format string `json:"format,omitempty"`
	// MaxBytes, when positive, overrides the resolver's default byte cap on the
	// referenced file (the cap itself always stands — an override moves it, never
	// disables it). Non-secret, so it rides the reference wherever it travels.
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// Validate checks the reference is well-formed: exactly one of File/Env is set,
// and Format is one of the known credential encodings. It validates shape only;
// resolving the reference (and reading the file/env) is a layer above the domain.
func (r CredentialSourceRef) Validate() error {
	hasFile, hasEnv := r.File != "", r.Env != ""
	if hasFile == hasEnv {
		return fmt.Errorf("credential source: set exactly one of file or env")
	}
	switch r.Format {
	case "csv", "jsonl", "tokens":
	default:
		return fmt.Errorf("credential source: unknown format %q (want csv, jsonl or tokens)", r.Format)
	}
	if r.MaxBytes < 0 {
		return fmt.Errorf("credential source: maxBytes must be positive (zero means the default cap)")
	}
	return nil
}

// CredentialPool supplies credentials to virtual users.
type CredentialPool struct {
	ID       ID                 `json:"id"`
	Strategy CredentialStrategy `json:"strategy"`
	Entries  []Credential       `json:"entries,omitempty"`
	// Source, when set, points at an external credential pool (a file or an env
	// var) instead of inlining Entries. It is layer-agnostic: the domain validates
	// only the reference's shape, never reads it, and a layer above (the CLI's
	// scenariofile.Expand) resolves it into Entries before a run. omitempty keeps
	// an Entries-only pool serializing byte-identically to before this field
	// existed.
	Source          *CredentialSourceRef `json:"source,omitempty"`
	BootstrapFlowID *ID                  `json:"bootstrapFlowId,omitempty"`
	// SignupFlow is the declarative bootstrap-signup journey (signup steps + a
	// capture mapping + an optional teardown journey) a CredBootstrapSignup pool
	// walks once per virtual user to provision a real account and capture its token.
	// It is transport-free; the orchestrator compiles it to a graph + templates at
	// provider-build time. Required (in place of the legacy BootstrapFlowID) for a
	// runnable bootstrap pool; ignored by other strategies. omitempty keeps a
	// non-bootstrap pool serializing byte-identically.
	SignupFlow *SignupFlow `json:"signupFlow,omitempty"`
	// KeepAccounts opts a bootstrap-signup run out of teardown: the provisioned
	// accounts are left in place after the run instead of being deprovisioned. It is
	// the ONLY escape from the gating-safety rule that a bootstrap pool without a
	// teardown flow is rejected — and it lets a kept-accounts run reproduce later
	// under the same still-live principals. Ignored by other strategies. omitempty
	// keeps a non-bootstrap pool serializing byte-identically.
	KeepAccounts bool `json:"keepAccounts,omitempty"`
	// LoginFlowID names the standalone login flow a CredLogin pool walks to mint a
	// token (POST a login/token endpoint, capture the token). It is a declarative
	// reference, not a node in the main scenario graph, so the simulated traffic
	// never observes the login. Required for the CredLogin strategy, ignored
	// otherwise. omitempty keeps a non-login pool serializing byte-identically.
	LoginFlowID *ID `json:"loginFlowId,omitempty"`
	// LoginScope selects how many principals a CredLogin pool mints: per-user (the
	// default, empty value) runs the login once per virtual user; shared runs it
	// once and shares the single token (client_credentials). Ignored by other
	// strategies. omitempty keeps a non-login pool serializing byte-identically.
	LoginScope LoginScope `json:"loginScope,omitempty"`
	// Mint configures the CredMint strategy: how to self-issue a JWT per virtual user
	// by signing one locally with a key the operator holds (the M1 case). It carries
	// the alg, the NON-SECRET signing-key reference, the claims template, the subject
	// source and the TTL — never the key itself (the resolved bytes ride on
	// MintSpec.resolvedKey, json:"-", AD-011). Required for the CredMint strategy,
	// ignored otherwise. omitempty keeps a non-mint pool serializing byte-identically.
	Mint *MintSpec `json:"mint,omitempty"`
	// Exec configures the CredExec strategy: run an operator-supplied COMMAND per
	// virtual user and use its stdout as the credential (the bring-your-own-token
	// escape hatch). It carries the argv command, optional extra env, a per-invocation
	// timeout and an output cap — and NO secret of its own (operator secrets ride in
	// Env values). Required for the CredExec strategy, ignored otherwise. omitempty
	// keeps a non-exec pool serializing byte-identically.
	Exec *ExecSpec `json:"exec,omitempty"`
}

// Validate checks the pool can actually provide credentials. For the pool
// strategy, exactly one of Entries or Source must be present: both-empty is the
// long-standing "needs an entry" error, and both-set is a new conflict error. A
// present Source is validated for shape but never rejected for being unresolved —
// that is a concern for the layer that runs the pool, not the domain.
func (c CredentialPool) Validate() error {
	if !c.Strategy.Valid() {
		return fmt.Errorf("credential pool %q: invalid strategy %q", c.ID, c.Strategy)
	}
	if c.Strategy == CredPool {
		hasEntries, hasSource := len(c.Entries) > 0, c.Source != nil
		switch {
		case !hasEntries && !hasSource:
			return fmt.Errorf("credential pool %q: pool strategy needs at least one entry", c.ID)
		case hasEntries && hasSource:
			return fmt.Errorf("credential pool %q: pool strategy takes either inline entries or a source, not both", c.ID)
		}
		if c.Source != nil {
			if err := c.Source.Validate(); err != nil {
				return fmt.Errorf("credential pool %q: %w", c.ID, err)
			}
		}
	}
	if c.Strategy == CredBootstrapSignup {
		// A runnable bootstrap pool carries a declarative SignupFlow (the form the
		// orchestrator compiles and walks); the legacy BootstrapFlowID is still
		// accepted as a bare reference. Require at least one, and validate a present
		// SignupFlow's shape (a resolvable token/secret capture above all).
		hasFlow := c.SignupFlow != nil
		hasLegacyID := c.BootstrapFlowID != nil && *c.BootstrapFlowID != ""
		if !hasFlow && !hasLegacyID {
			return fmt.Errorf("credential pool %q: bootstrap-signup needs a signupFlow (or a bootstrapFlowId)", c.ID)
		}
		if hasFlow {
			if err := c.SignupFlow.Validate(); err != nil {
				return fmt.Errorf("credential pool %q: %w", c.ID, err)
			}
		}
	}
	if c.Strategy == CredLogin {
		if c.LoginFlowID == nil || *c.LoginFlowID == "" {
			return fmt.Errorf("credential pool %q: login strategy needs a non-empty loginFlowId", c.ID)
		}
		// The empty scope is the per-user default; any explicit value must be known.
		if c.LoginScope != "" && !c.LoginScope.Valid() {
			return fmt.Errorf("credential pool %q: invalid loginScope %q (want %q or %q)", c.ID, c.LoginScope, LoginPerUser, LoginShared)
		}
		// P8: a login pool MAY carry a credential pool of login-INPUT rows — each
		// Credential is interpreted as Subject=username, Secret=password (NOT a
		// pre-issued token), so virtual user i logs in as a different account by
		// templating the row (see api.NewLoginTokenFunc, which seeds {{.username}}/
		// {{.password}}). Entries and a Source are mutually exclusive (like the pool
		// strategy): each is a way to supply the SAME input rows. Both empty is the
		// long-standing single-identity login — accepted, unchanged. Inline passwords
		// are in-process secrets exactly like the token pool's inline entries; the
		// Credential.Secret json:"-" tag keeps them out of any serialization (AD-011),
		// and the distributed-login follow-up keeps login+workers rejected at the run
		// path so those passwords never cross the wire.
		hasEntries, hasSource := len(c.Entries) > 0, c.Source != nil
		if hasEntries && hasSource {
			return fmt.Errorf("credential pool %q: login strategy takes either inline login-input entries or a source, not both", c.ID)
		}
		if c.Source != nil {
			if err := c.Source.Validate(); err != nil {
				return fmt.Errorf("credential pool %q: %w", c.ID, err)
			}
		}
	}
	if c.Strategy == CredMint {
		// A mint pool self-issues a JWT per virtual user; it carries no entries/source/
		// login/signup flow (the token is signed locally), only a MintSpec describing
		// the alg, the key reference, the claims and the TTL.
		if c.Mint == nil {
			return fmt.Errorf("credential pool %q: mint strategy needs a mint block (alg, key reference, ttl)", c.ID)
		}
		if err := c.Mint.Validate(); err != nil {
			return fmt.Errorf("credential pool %q: %w", c.ID, err)
		}
	}
	if c.Strategy == CredExec {
		// An exec pool runs an operator-supplied command per virtual user and uses its
		// stdout as the credential; it carries no entries/source/login/signup flow (the
		// token comes from the command), only an ExecSpec describing the argv, the env,
		// the timeout and the output cap. The operator opt-in that actually permits the
		// command to run is a RUN-PATH gate (StartRun), not a pool-shape check — Validate
		// only confirms the spec is runnable.
		if c.Exec == nil {
			return fmt.Errorf("credential pool %q: exec strategy needs an exec block (command, timeout)", c.ID)
		}
		if err := c.Exec.Validate(); err != nil {
			return fmt.Errorf("credential pool %q: %w", c.ID, err)
		}
	}
	return nil
}

// EffectiveLoginScope resolves the pool's login scope, defaulting an empty value
// to per-user. It is meaningful only for a CredLogin pool.
func (c CredentialPool) EffectiveLoginScope() LoginScope {
	if c.LoginScope == "" {
		return LoginPerUser
	}
	return c.LoginScope
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
	RunID    ID              `json:"runId"`
	Category FindingCategory `json:"category"`
	Severity Severity        `json:"severity"`
	// EvidenceRef is the stable identity of what the finding is about: the API
	// id for per-endpoint findings, the metric name (e.g. "error-rate") for
	// threshold findings, "run-wide" for summary-derived aggregates. It carries
	// no run-specific numbers, so the same issue keys identically across runs.
	EvidenceRef string    `json:"evidenceRef,omitempty"`
	FirstSeen   time.Time `json:"firstSeen"`
	Description string    `json:"description"`
	// Count is the number of occurrences behind the finding (errors surfaced,
	// contract violations, length of the failure streak). Zero when the
	// category has no occurrence count (threshold findings carry rates, which
	// stay in the description).
	Count int `json:"count,omitempty"`
	// Evidence, when present, is the diagnostic bundle behind the finding:
	// representative sessions with reproduce coordinates, the status-code
	// distribution and the failure timing across the run window. It is
	// optional (omitempty) so findings persisted before the bundle existed —
	// and the coarse summary-derived findings, which retain no per-request
	// data — round-trip unchanged.
	Evidence *FindingEvidence `json:"evidence,omitempty"`
	// RootCauseClass is the verdict `tmula reproduce` stamps after replaying
	// one of the finding's evidence sessions in isolation: RootCauseFunctional
	// when every attempt reproduced the failure without load,
	// RootCauseLoadDependent when none did, RootCauseFlaky in between. It is a
	// signal, not a proof — the replay recreates the traffic composition, not
	// the original timing or target state. Empty (omitted on the wire) until a
	// reproduce pass runs, so legacy findings round-trip unchanged.
	RootCauseClass string `json:"rootCauseClass,omitempty"`
}

// Root-cause classes a reproduce pass can stamp on a finding (see
// Finding.RootCauseClass).
const (
	// RootCauseFunctional: the failure reproduced on every isolated attempt —
	// it does not need load, so it is likely a plain functional bug.
	RootCauseFunctional = "functional"
	// RootCauseLoadDependent: the failure reproduced on no isolated attempt —
	// it likely needs the original concurrency/saturation to manifest.
	RootCauseLoadDependent = "load-dependent"
	// RootCauseFlaky: the failure reproduced on some attempts only.
	RootCauseFlaky = "flaky"
)

// FindingEvidence is the diagnostic bundle attached to a finding so it can be
// acted on without re-running the experiment: which sessions hit the issue
// (and how to replay them), which status codes came back, and when in the run
// the occurrences clustered.
//
// JSON field naming here is part of the masking contract: the shared-report
// path (api/share.go) runs the deny-by-default PII masker over the report
// JSON, and that masker redacts any field whose NAME contains a sensitive
// substring — including "session". Session and node ids are synthetic tmula
// identifiers, not PII, so the wire names below ("vus", "vu", "path", ...)
// are deliberately chosen to carry them past the masker intact.
type FindingEvidence struct {
	// Sessions are up to a handful of representative sessions behind the
	// finding, chosen for diagnosability (the earliest occurrences plus the
	// slowest of the rest) rather than arrival order. Empty when the producing
	// path could not attribute requests to sessions.
	Sessions []EvidenceSession `json:"vus,omitempty"`
	// TimeBuckets distribute the finding's occurrences over four fixed
	// quarters of the observed run window, so a report shows whether they
	// cluster early in ramp-up or late in soak. Omitted when no occurrence
	// carried a timestamp.
	TimeBuckets []EvidenceBucket `json:"timeBuckets,omitempty"`
	// StatusCounts tallies the HTTP status codes of every occurrence (not just
	// the representative sessions). Transport-level failures carry no code and
	// are visible through the sessions' ErrorClass instead.
	StatusCounts map[int]int `json:"statusCounts,omitempty"`
}

// EvidenceSession is one representative session behind a finding: its
// identity (the X-Tmula-Session-ID correlation value to grep server logs
// for), the seed coordinates a reproduce command replays it from, the journey
// it walked, and the request that surfaced the issue.
type EvidenceSession struct {
	// SessionID is the virtual-user/session label, sent to the target as the
	// X-Tmula-Session-ID header on every request the session made.
	SessionID string `json:"vu"`
	// Seed is the session's walk seed and UserIndex the offset that derives it
	// from the run seed (run seed + UserIndex == Seed): the pool index for the
	// closed model, the arrival number for the open model, the global user
	// index for a distributed shard.
	Seed      int64 `json:"seed"`
	UserIndex int64 `json:"userIndex"`
	// Persona is the segment label the session was drawn from ("" when the run
	// had no persona mix).
	Persona string `json:"persona,omitempty"`
	// Path is the node sequence the session traversed up to and including the
	// request below. Empty when the producing path does not carry journeys
	// (the distributed stream ships no per-request path).
	Path []ID `json:"path,omitempty"`
	// The request that surfaced the issue: status (0 for transport-level
	// failures), latency, error class and completion time.
	StatusCode int       `json:"statusCode,omitempty"`
	LatencyMs  float64   `json:"latencyMs"`
	ErrorClass string    `json:"errorClass,omitempty"`
	TS         time.Time `json:"ts"`
}

// EvidenceBucket is one fixed quarter of the run window and how many of the
// finding's occurrences fell into it.
type EvidenceBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
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
