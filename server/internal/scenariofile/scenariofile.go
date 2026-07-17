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
	"regexp"
	"strings"
	"time"

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
	// SuggestedSignup is a signup-authoring block the importer derives from a
	// register/signup operation, offered to the UI as a "create test accounts"
	// suggestion that is INDEPENDENT of Auth (Auth may be a login while a signup is
	// suggested separately). Expand maps it onto the spec's SuggestedSignup
	// (a domain.SignupFlow) without making it the run's auth. It carries no secret —
	// the token is captured from the live signup response. Omit it for no suggestion.
	SuggestedSignup *AuthSignup `json:"suggestedSignup,omitempty"`
	// AuthAdvisories are import-time hints about the document's auth the importer
	// could not act on (managed-IdP mint footgun, openIdConnect discovery pointer).
	// Expand carries them onto the spec verbatim; the run path never reads them.
	AuthAdvisories []domain.AuthAdvisory `json:"authAdvisories,omitempty"`
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
	// Users is the pool of credential rows, assigned to virtual users by index
	// (wrapping around when there are more users than rows). Its meaning depends on
	// the strategy: for "pool" each row is a PRE-SUPPLIED credential (subject + a
	// ready-to-use token); for "login" (P8 multi-user login) each row is a login
	// INPUT — subject is the username and token is the PASSWORD — so virtual user i
	// logs in as row i%N and mints its own token (the login flow templates
	// {{.username}}/{{.password}}). Either way the token field is a secret authored
	// in the file and masked at rest (the domain credential's secret is json:"-").
	Users []Credential `json:"users,omitempty"`
	// Source, when set, names an external credential pool instead of inlining
	// Users: a file (resolved against the scenario file's directory) or an
	// environment variable, in an explicit format. Mutually exclusive with Users.
	// It carries the same rows Users would (tokens for "pool", username/password
	// login inputs for "login").
	Source *AuthSource `json:"source,omitempty"`
	// UsersPattern, when set, GENERATES the credential rows from a subject/token
	// template pair and a count (subject "user{{.userIndex}}", token
	// "pw-{{.userIndex}}", count 100000) instead of authoring or loading them —
	// so tens of thousands of accounts need no file. Mutually exclusive with Users
	// and Source. Materialized into entries at Expand time; the token template is
	// a secret and never rides the wire (see auth.GeneratorSource).
	UsersPattern *AuthUsersPattern `json:"usersPattern,omitempty"`
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
	// Mint, when set (strategy "mint"), describes how to self-issue a JWT per virtual
	// user by signing one locally with a key the operator holds (the M1 case): the
	// alg, the NON-SECRET signing-key reference, the claims template, the subject and
	// the TTL. It carries no secret in the file — the key is a reference (env var or
	// a file) resolved in-process at run time, never inlined.
	Mint *AuthMint `json:"mint,omitempty"`
	// Exec, when set (strategy "exec"), describes a COMMAND run per virtual user whose
	// stdout becomes the token — the bring-your-own-token escape hatch for auth tmula
	// cannot model declaratively (social/SDK login, third-party IdP consent). It carries
	// no secret in the file: operator secrets go in Env values (which may reference host
	// env), never serialized. A run requires an explicit operator opt-in because it
	// executes an arbitrary local command.
	Exec *AuthExec `json:"exec,omitempty"`
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
// that become the credential, and an optional scope. The login block itself carries
// no secret — the token comes from the live login response. P8 multi-user login:
// the SURROUNDING auth.users / auth.source (not this block) may supply login-INPUT
// rows (username/password) so each virtual user logs in as a different account; the
// login flow templates {{.username}}/{{.password}} from the row it is minting for.
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
	// Refresh, when set, is an OPTIONAL explicit refresh-grant override. When present it
	// WINS over tmula's auto-derivation of a grant_type=refresh_token request from the
	// login's form grant — so even a JSON-body login (which tmula cannot auto-rewrite)
	// gets a real refresh exchange instead of re-running the login on a mid-run 401. Omit
	// it and tmula auto-derives (for an OAuth2 form login) or re-logins (otherwise),
	// unchanged. It carries no secret — the refresh token is captured at run time.
	Refresh *AuthLoginRefresh `json:"refresh,omitempty"`
}

// AuthLoginRefresh authors an explicit refresh-grant override for the login strategy:
// the request line the refresh POSTs to and the form body it sends. It SHORT-CIRCUITS
// tmula's auto-derivation, so a login whose token POST is not an OAuth2 form grant can
// still refresh without re-logging-in. It carries no secret — the refresh token is
// captured from the live login response and templated in via {{.refreshToken}}.
type AuthLoginRefresh struct {
	// Request is the "METHOD /path" the refresh grant POSTs to, e.g. POST /oauth/token.
	// OPTIONAL: when omitted the refresh reuses the login token endpoint (a same-endpoint
	// refresh needs only a body).
	Request string `json:"request,omitempty"`
	// Body is the refresh request body — usually a form grant such as
	// "grant_type=refresh_token&refresh_token={{.refreshToken}}&client_id=…". The
	// {{.refreshToken}} marker is filled with the captured refresh token at run time
	// (and url-encoded so an opaque token stays form-safe).
	Body string `json:"body,omitempty"`
}

// AuthMint authors the mint (local JWT signing) credential strategy: how to
// self-issue a JWT per virtual user with a key the operator holds. It carries no
// secret — Key is a NON-SECRET reference (an env var or a file), resolved in-process
// at run time. Ttl/Leeway are authored as duration STRINGS ("1h", "30m", "5s") since
// time.Duration does not round-trip through YAML/JSON as a human duration.
type AuthMint struct {
	// Alg is the JWS signing algorithm: HS256, RS256 or ES256.
	Alg string `json:"alg"`
	// SecretEncoding declares how the HS256 secret body is encoded (raw | base64 |
	// base64url). Required for HS256, ignored for the asymmetric algs.
	SecretEncoding string `json:"secretEncoding,omitempty"`
	// Key is the non-secret signing-key reference: an env var or a file (resolved
	// against the scenario file's directory). For HS256 it points at the symmetric
	// secret; for RS256/ES256 at a PEM private key.
	Key *AuthMintKey `json:"key,omitempty"`
	// Subject is the per-VU sub-claim template, e.g. "user-{{.userIndex}}".
	Subject string `json:"subject,omitempty"`
	// Claims is a template map signed into every token; values may reference
	// {{.userIndex}} and {{.subject}}.
	Claims map[string]string `json:"claims,omitempty"`
	// Ttl is the token lifetime as a duration string ("1h"); it becomes exp = now+ttl.
	Ttl string `json:"ttl"`
	// Leeway, when set (a duration string), shifts an otherwise-off nbf to now-leeway.
	Leeway string `json:"leeway,omitempty"`
}

// AuthMintKey names where the signing key lives: an env var or a file. Exactly one is
// set. It carries no secret — only the pointer to it.
type AuthMintKey struct {
	// File is a path to the key file, resolved against the scenario file's directory.
	// Mutually exclusive with Env.
	File string `json:"file,omitempty"`
	// Env names the environment variable holding the key. Mutually exclusive with File.
	Env string `json:"env,omitempty"`
}

// AuthExec authors the exec (bring-your-own-token) credential strategy: an operator-
// supplied COMMAND run per virtual user whose stdout becomes the token — the universal
// escape hatch for auth tmula cannot model declaratively. It carries no secret in the
// file: operator secrets belong in Env values (which may reference host env), never
// serialized as part of the run. Timeout is a duration STRING ("10s") since
// time.Duration does not round-trip through YAML/JSON as a human duration.
//
// SECURITY: exec runs an arbitrary local command, so a run is gated behind an explicit
// OPERATOR opt-in (a scenario file alone never executes anything), the command is
// argv-only (no shell — a metacharacter in an arg is passed literally), and its egress
// is NOT bound by the target allowlist / rate cap. See domain.ExecSpec.
type AuthExec struct {
	// Command is the argv to run: argv[0] is the program, the rest are its arguments,
	// passed literally (NOT a shell string). Elements may reference {{.userIndex}}.
	Command []string `json:"command"`
	// Env is extra environment passed to the child (where operator secrets belong, NOT
	// argv which is visible via ps); values may reference {{.userIndex}} and host env.
	Env map[string]string `json:"env,omitempty"`
	// Timeout is the per-invocation timeout as a duration string ("10s"). Optional — a
	// sane default applies when empty; a hung command is always bounded.
	Timeout string `json:"timeout,omitempty"`
	// MaxOutputBytes caps the stdout read so a runaway command cannot OOM the run.
	// Optional — a sane default applies when zero.
	MaxOutputBytes int `json:"maxOutputBytes,omitempty"`
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
	// MaxBytes, when positive, overrides the default byte cap on the referenced
	// file (the cap itself always stands — an override moves it, never disables
	// it). Zero means the default (512 MiB).
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// AuthUsersPattern generates credential rows from templates instead of authoring
// or loading them: Subject and Token are Go text/template strings rendered with
// {{.userIndex}} for i=0..Count-1. For "pool" the Token is a ready secret
// (typically only useful for non-opaque secrets — an opaque JWT cannot be
// patterned, that is mint's job); for "login" it is the password each generated
// username logs in with. It carries the same subject/token vocabulary as an
// inline Credential.
type AuthUsersPattern struct {
	// Subject is the per-row subject template; empty yields empty subjects.
	Subject string `json:"subject,omitempty"`
	// Token is the per-row secret/password template (required).
	Token string `json:"token"`
	// Count is how many rows to generate (positive, within the pool ceiling).
	Count int `json:"count"`
}

// Credential is one pre-supplied principal in the compact file: a non-sensitive
// subject and its secret token. It is distinct from domain.Credential precisely
// so the token can be authored in the file (the domain type hides its secret from
// serialization); Expand copies Token into the domain credential's secret.
type Credential struct {
	// Subject is the non-sensitive principal id, exposed to templates as
	// {{.subject}}. For the "pool" strategy it is a credential's principal id; for
	// the "login" strategy (P8) it is the USERNAME virtual user i logs in with
	// (also exposed as {{.username}}).
	Subject string `json:"subject,omitempty"`
	// Token is the secret auth material authored in the file. For the "pool"
	// strategy it is a ready-to-use token (e.g. a JWT), exposed to templates as
	// {{.token}}; for the "login" strategy (P8) it is the PASSWORD the login flow
	// posts (exposed as {{.password}}, with {{.secret}} as an alias). Either way it
	// lives only in the authored file; the domain credential it maps to never
	// serializes its secret.
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

	// The suggested signup is advisory (a "create test accounts" offer), independent
	// of the primary auth above: it rides the spec untouched by the run path. Build
	// it with the SAME helper the bootstrap-signup strategy uses so the two agree on
	// the domain shape.
	if s.SuggestedSignup != nil {
		flow, err := buildSignupFlow(s.SuggestedSignup)
		if err != nil {
			return runspec.RunSpec{}, err
		}
		spec.SuggestedSignup = flow
	}

	// Auth advisories are advisory-only import hints; carry them verbatim so the
	// /import response can surface them.
	if len(s.AuthAdvisories) > 0 {
		spec.AuthAdvisories = append([]domain.AuthAdvisory(nil), s.AuthAdvisories...)
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
	// An importer-scaffolded auth block ships REPLACE_ME_* placeholders where a real
	// secret must go. Reject a still-unfilled placeholder HERE — at expand time, before
	// a run boots — naming the exact location, so a scenario never silently authenticates
	// with the literal "REPLACE_ME_PASSWORD" and fails deep in the login flow instead.
	if err := checkNoReplaceMe(a); err != nil {
		return domain.CredentialPool{}, nil, err
	}
	strategy := domain.CredentialStrategy(a.Strategy)
	if a.Strategy == "" {
		strategy = domain.CredPool
	}
	switch strategy {
	case domain.CredPool:
		pool, err := buildPoolCredentials(a, dir, keepSourceRef)
		return pool, nil, err
	case domain.CredLogin:
		return buildLoginCredentials(a, dir)
	case domain.CredBootstrapSignup:
		pool, err := buildBootstrapCredentials(a)
		return pool, nil, err
	case domain.CredMint:
		pool, err := buildMintCredentials(a, dir, keepSourceRef)
		return pool, nil, err
	case domain.CredExec:
		pool, err := buildExecCredentials(a)
		return pool, nil, err
	default:
		return domain.CredentialPool{}, nil, fmt.Errorf("scenariofile: auth strategy %q is not supported (use %q with pre-supplied users or a source, %q with a login flow, %q with a signup flow, %q to self-issue a JWT, or %q to mint a token from a command)", strategy, domain.CredPool, domain.CredLogin, domain.CredBootstrapSignup, domain.CredMint, domain.CredExec)
	}
}

// replaceMePlaceholderRE matches an unfilled importer placeholder — the literal
// REPLACE_ME plus any trailing SCREAMING_SNAKE suffix (REPLACE_ME_PASSWORD,
// REPLACE_ME_TOKEN, …) — so the guard can name the exact token an operator forgot
// to fill instead of a bare "REPLACE_ME".
var replaceMePlaceholderRE = regexp.MustCompile(`REPLACE_ME[A-Z0-9_]*`)

// firstReplaceMe returns the first REPLACE_ME* placeholder token in s, or "".
func firstReplaceMe(s string) string { return replaceMePlaceholderRE.FindString(s) }

// firstReplaceMeInSteps scans each step's body and header values for an unfilled
// placeholder, returning the owning step id and the exact token found (or "","").
func firstReplaceMeInSteps(steps []Step) (stepID, token string) {
	for _, st := range steps {
		if tok := firstReplaceMe(st.Body); tok != "" {
			return st.ID, tok
		}
		for _, hv := range st.Headers {
			if tok := firstReplaceMe(hv); tok != "" {
				return st.ID, tok
			}
		}
	}
	return "", ""
}

// checkNoReplaceMe rejects an auth block that still carries an importer REPLACE_ME_*
// placeholder anywhere a secret belongs — inline pool tokens, a usersPattern token, a
// login/signup flow body or header, or an exec command argv/env value. The error names
// the exact location and the placeholder token, so an operator sees precisely what to
// fill (or that --auth-source can supply the credential without editing the file). The
// non-secret key REFERENCES (auth.mint.key, auth.source) are intentionally NOT scanned —
// a REPLACE_ME there is a reference name, not a leaked-through secret placeholder.
func checkNoReplaceMe(a Auth) error {
	const hint = " — fill it in or pass --auth-source"
	for i, u := range a.Users {
		if tok := firstReplaceMe(u.Token); tok != "" {
			return fmt.Errorf("scenariofile: auth.users[%d].token still contains %s%s", i, tok, hint)
		}
		if tok := firstReplaceMe(u.Subject); tok != "" {
			return fmt.Errorf("scenariofile: auth.users[%d].subject still contains %s%s", i, tok, hint)
		}
	}
	if a.UsersPattern != nil {
		if tok := firstReplaceMe(a.UsersPattern.Token); tok != "" {
			return fmt.Errorf("scenariofile: auth.usersPattern.token still contains %s%s", tok, hint)
		}
		if tok := firstReplaceMe(a.UsersPattern.Subject); tok != "" {
			return fmt.Errorf("scenariofile: auth.usersPattern.subject still contains %s%s", tok, hint)
		}
	}
	if a.Login != nil {
		if id, tok := firstReplaceMeInSteps(a.Login.Flow); tok != "" {
			return fmt.Errorf("scenariofile: auth.login.flow step %q still contains %s%s", id, tok, hint)
		}
	}
	if a.Signup != nil {
		if id, tok := firstReplaceMeInSteps(a.Signup.Flow); tok != "" {
			return fmt.Errorf("scenariofile: auth.signup.flow step %q still contains %s%s", id, tok, hint)
		}
		// Teardown runs with the same secret-bearing shape as the flow — an unfilled
		// placeholder there 401s every deprovision and leaks the provisioned accounts.
		if id, tok := firstReplaceMeInSteps(a.Signup.Teardown); tok != "" {
			return fmt.Errorf("scenariofile: auth.signup.teardown step %q still contains %s%s", id, tok, hint)
		}
	}
	if a.Exec != nil {
		for _, arg := range a.Exec.Command {
			if tok := firstReplaceMe(arg); tok != "" {
				return fmt.Errorf("scenariofile: auth.exec.command still contains %s%s", tok, hint)
			}
		}
		for k, v := range a.Exec.Env {
			if tok := firstReplaceMe(v); tok != "" {
				return fmt.Errorf("scenariofile: auth.exec.env[%q] still contains %s%s", k, tok, hint)
			}
		}
	}
	return nil
}

// buildExecCredentials maps the exec authoring block onto an exec pool carrying a
// domain.ExecSpec (the argv command, the extra env, the timeout and the output cap). It
// carries no secret — operator secrets go in Env values (which may reference host env),
// never serialized as part of the run. The shape is validated here so a malformed exec
// block is rejected at expand time with a clear, scenariofile-prefixed message rather
// than deferred to the run path. exec is mutually exclusive with the pool-shaped inputs
// (inline users / a source) and the other strategies' blocks.
func buildExecCredentials(a Auth) (domain.CredentialPool, error) {
	if len(a.Users) > 0 || a.Source != nil || a.UsersPattern != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: the %q strategy mints its own token from a command and takes no inline users or source", domain.CredExec)
	}
	if a.Login != nil || a.Mint != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth.login and auth.mint are not valid with the %q strategy", domain.CredExec)
	}
	if a.Exec == nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: the %q strategy needs an auth.exec block (a command argv, optional env/timeout)", domain.CredExec)
	}
	e := a.Exec
	spec := domain.ExecSpec{
		Command:        e.Command,
		Env:            e.Env,
		MaxOutputBytes: e.MaxOutputBytes,
	}
	if s := strings.TrimSpace(e.Timeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth.exec.timeout %q: %w", e.Timeout, err)
		}
		spec.Timeout = d
	}
	// Validate the shape here (a non-empty command argv, non-negative timeout/cap) so a
	// malformed exec block is rejected at expand time with a clear, scenariofile-prefixed
	// message — not deferred to the run path.
	if err := spec.Validate(); err != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: %w", err)
	}
	return domain.CredentialPool{ID: "cli-pool", Strategy: domain.CredExec, Exec: &spec}, nil
}

// buildMintCredentials maps the mint authoring block onto a mint pool carrying a
// domain.MintSpec (alg, the NON-SECRET signing-key reference, the claims template,
// the subject and the TTL). It carries no secret — the signing key is a reference
// resolved in-process at run time. The durations are authored as strings ("1h") and
// parsed here; the full validation (signable alg, positive TTL, present key ref, HS
// encoding) runs in domain.MintSpec.Validate via runspec.Validate, so this builder
// only translates the authored block into the domain shape and parses the durations.
func buildMintCredentials(a Auth, dir string, keepSourceRef bool) (domain.CredentialPool, error) {
	if len(a.Users) > 0 || a.Source != nil || a.UsersPattern != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: the %q strategy self-issues a JWT and takes no inline users or source", domain.CredMint)
	}
	if a.Login != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth.login is only valid with the %q strategy", domain.CredLogin)
	}
	if a.Mint == nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: the %q strategy needs an auth.mint block (alg, key reference, ttl)", domain.CredMint)
	}
	m := a.Mint
	ttl, err := time.ParseDuration(strings.TrimSpace(m.Ttl))
	if err != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth.mint.ttl %q: %w", m.Ttl, err)
	}
	var leeway time.Duration
	if s := strings.TrimSpace(m.Leeway); s != "" {
		leeway, err = time.ParseDuration(s)
		if err != nil {
			return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth.mint.leeway %q: %w", m.Leeway, err)
		}
	}
	spec := domain.MintSpec{
		Alg:            domain.MintAlg(m.Alg),
		SecretEncoding: domain.MintEncoding(m.SecretEncoding),
		Subject:        m.Subject,
		Claims:         m.Claims,
		TTL:            ttl,
		Leeway:         leeway,
	}
	if m.Key != nil {
		spec.Key = &domain.CredentialSourceRef{File: m.Key.File, Env: m.Key.Env}
	}
	// Validate the shape here (signable alg, positive TTL, present key reference, HS
	// encoding) so a malformed mint block is rejected at expand time with a clear,
	// scenariofile-prefixed message — not deferred to the run path.
	if err := spec.Validate(); err != nil {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: %w", err)
	}
	// Record the scenario file's directory as the ROOT a relative key.file resolves
	// against at run time — the same way auth.source.file is rooted — so the documented
	// "resolved against the scenario file's directory" contract holds instead of the key
	// resolving against the process CWD. The key itself is still resolved lazily (the
	// reference need not be present at expand time); keyRoot is non-secret and json:"-",
	// so it never crosses the wire — a distributed worker resolves against its OWN root.
	// keepSourceRef (a distributed engine) leaves keyRoot empty: the worker owns the root.
	if !keepSourceRef {
		spec = spec.WithKeyRoot(dir)
	}
	return domain.CredentialPool{ID: "cli-pool", Strategy: domain.CredMint, Mint: &spec}, nil
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
	hasUsers, hasSource, hasPattern := len(a.Users) > 0, a.Source != nil, a.UsersPattern != nil
	if err := exactlyOneAuthRowSource(hasUsers, hasSource, hasPattern); err != nil {
		return domain.CredentialPool{}, err
	}
	if !hasUsers && !hasSource && !hasPattern {
		return domain.CredentialPool{}, fmt.Errorf("scenariofile: auth needs inline users or a source for the %q strategy", domain.CredPool)
	}

	if hasSource && keepSourceRef {
		// Ship the reference unresolved (distributed engine path): validate its
		// shape but do not read it — the engine's workers load it locally.
		ref := domain.CredentialSourceRef{File: a.Source.File, Env: a.Source.Env, Format: a.Source.Format, MaxBytes: a.Source.MaxBytes}
		if err := ref.Validate(); err != nil {
			return domain.CredentialPool{}, fmt.Errorf("scenariofile: %w", err)
		}
		return domain.CredentialPool{ID: "cli-pool", Strategy: domain.CredPool, Source: &ref}, nil
	}

	entries, err := resolveAuthRows(a, dir)
	if err != nil {
		return domain.CredentialPool{}, err
	}
	return domain.CredentialPool{ID: "cli-pool", Strategy: domain.CredPool, Entries: entries}, nil
}

// resolveAuthRows resolves the auth block's credential rows from EITHER inline
// Users (each authored Subject/Token maps onto a domain credential's subject/secret)
// OR an external Source (a file under dir, or an env var) loaded HERE. It is the
// single resolution point shared by the pool strategy (tokens) and the login
// strategy (login-INPUT rows: subject=username, token=password) — both read the
// SAME row shape, only the interpretation differs. The caller has already enforced
// mutual exclusion and that at least one is present where required; this helper
// assumes exactly one of Users/Source is set when it is asked to resolve.
func resolveAuthRows(a Auth, dir string) ([]domain.Credential, error) {
	if len(a.Users) > 0 {
		entries := make([]domain.Credential, len(a.Users))
		for i, c := range a.Users {
			entries[i] = domain.Credential{Subject: c.Subject, Secret: c.Token}
		}
		return entries, nil
	}
	if a.UsersPattern != nil {
		// Generate the rows from the pattern, materialized here (never carried as a
		// reference — the token template is a secret).
		gen, err := auth.NewGeneratorSource(a.UsersPattern.Subject, a.UsersPattern.Token, a.UsersPattern.Count)
		if err != nil {
			return nil, fmt.Errorf("scenariofile: auth.usersPattern: %w", err)
		}
		entries, err := gen.Load(context.Background())
		if err != nil {
			return nil, fmt.Errorf("scenariofile: auth.usersPattern: %w", err)
		}
		return entries, nil
	}
	src, err := credentialSourceFor(*a.Source, dir)
	if err != nil {
		return nil, err
	}
	entries, err := src.Load(context.Background())
	if err != nil {
		return nil, fmt.Errorf("scenariofile: %w", err)
	}
	return entries, nil
}

// exactlyOneAuthRowSource rejects a conflict among the three ways to supply
// credential rows (inline users, an external source, a generated pattern). At
// most one may be set; zero is allowed here (the caller decides whether that is
// an error for its strategy — the single-identity login accepts none).
func exactlyOneAuthRowSource(hasUsers, hasSource, hasPattern bool) error {
	n := 0
	for _, set := range []bool{hasUsers, hasSource, hasPattern} {
		if set {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("scenariofile: auth takes only one of inline users, a source, or a usersPattern")
	}
	return nil
}

// buildLoginCredentials compiles the login authoring block into a login pool plus
// the standalone login flow the orchestrator mints tokens from. The login flow is
// built with the SAME buildGraph/buildTemplates helpers the main flow uses, so a
// login journey is authored exactly like any other flow, and it never carries a
// secret (the token is minted at run time).
//
// P8 MULTI-USER LOGIN: the auth block MAY carry a pool of login-INPUT rows — inline
// auth.users (subject=username, token=password) or an external auth.source of the
// same shape. They are NOT pre-issued tokens: virtual user i logs in as row i%N, so
// each VU authenticates as a different account (the login flow templates
// {{.username}}/{{.password}}). The rows are resolved HERE into pool.Entries — the
// SAME single resolution point the pool strategy uses (resolveAuthRows), and a login
// source is ALWAYS resolved into entries (never kept as a reference): a login pool
// can never distribute (login+workers/--engine stays rejected because a minted token
// and the inline passwords are secrets the fan-out cannot resolve), so a login pool
// always arrives at the run path with Source nil and real in-process entries. With
// no users and no source, it is the unchanged single-identity login.
func buildLoginCredentials(a Auth, dir string) (domain.CredentialPool, *runspec.LoginFlowSpec, error) {
	hasUsers, hasSource, hasPattern := len(a.Users) > 0, a.Source != nil, a.UsersPattern != nil
	if err := exactlyOneAuthRowSource(hasUsers, hasSource, hasPattern); err != nil {
		return domain.CredentialPool{}, nil, err
	}
	var entries []domain.Credential
	if hasUsers || hasSource || hasPattern {
		// Login-input rows are always resolved to in-process entries here — a login
		// pool never ships a source reference to a distributed engine (login is
		// rejected with workers/--engine), so the run path always carries real rows.
		resolved, err := resolveAuthRows(a, dir)
		if err != nil {
			return domain.CredentialPool{}, nil, err
		}
		entries = resolved
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
		// The login-INPUT rows (empty for the single-identity login). The orchestrator
		// threads them into the login token func so VU i logs in as row i%N.
		Entries: entries,
	}
	loginFlow := &runspec.LoginFlowSpec{
		Graph:      graph,
		Templates:  templates,
		Start:      domain.ID(start),
		MaxSteps:   len(a.Login.Flow),
		TokenVar:   a.Login.Capture.Token,
		SubjectVar: a.Login.Capture.Subject,
	}
	// The OPTIONAL explicit refresh override threads onto the login flow spec: when set it
	// WINS over the orchestrator's auto-derivation (a JSON-body login still gets a real
	// refresh exchange). The request line is normalized to "METHOD /path" so the run-path
	// shape check and the transport agree on the form; an empty request defers to the
	// login token endpoint. It carries no secret — the refresh token is captured at run time.
	if a.Login.Refresh != nil {
		if req := strings.TrimSpace(a.Login.Refresh.Request); req != "" {
			method, path, err := parseRequest(req)
			if err != nil {
				return domain.CredentialPool{}, nil, fmt.Errorf("scenariofile: auth.login.refresh.request: %w", err)
			}
			loginFlow.RefreshRequest = method + " " + path
		}
		loginFlow.RefreshBody = a.Login.Refresh.Body
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
	if len(a.Users) > 0 || a.Source != nil || a.UsersPattern != nil {
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

	flow, err := buildSignupFlow(a.Signup)
	if err != nil {
		return domain.CredentialPool{}, err
	}
	return domain.CredentialPool{
		ID:           "cli-pool",
		Strategy:     domain.CredBootstrapSignup,
		SignupFlow:   flow,
		KeepAccounts: a.KeepAccounts,
	}, nil
}

// buildSignupFlow translates a signup-authoring block (the bootstrap-signup
// strategy's auth.signup, or the importer's standalone suggestion) into the
// declarative domain.SignupFlow the orchestrator compiles: signup steps + a
// capture mapping + an optional teardown journey. It carries no secret — the
// token is captured at run time. Shared by the bootstrap strategy and the
// advisory SuggestedSignup so the two agree on the domain shape.
func buildSignupFlow(sg *AuthSignup) (*domain.SignupFlow, error) {
	steps, err := buildSignupSteps(sg.Flow)
	if err != nil {
		return nil, err
	}
	flow := &domain.SignupFlow{
		Steps:   steps,
		Start:   domain.ID(sg.Start),
		Capture: domain.SignupCapture{Token: sg.Capture.Token, Subject: sg.Capture.Subject},
	}
	if len(sg.Teardown) > 0 {
		teardown, err := buildSignupSteps(sg.Teardown)
		if err != nil {
			return nil, err
		}
		flow.Teardown = teardown
		flow.TeardownStart = domain.ID(sg.TeardownStart)
	}
	return flow, nil
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
	return auth.FileSource{Root: root, Path: a.File, Format: format, MaxBytes: a.MaxBytes}, nil
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
