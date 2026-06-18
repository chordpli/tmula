package domain

// EnvClass classifies a target environment. prod is locked by default and
// requires an explicit unlock (policy §1) before any traffic is sent there.
type EnvClass string

const (
	EnvDev        EnvClass = "dev"
	EnvStaging    EnvClass = "staging"
	EnvProdLocked EnvClass = "prod-locked"
)

// Valid reports whether e is a known environment class.
func (e EnvClass) Valid() bool {
	switch e {
	case EnvDev, EnvStaging, EnvProdLocked:
		return true
	default:
		return false
	}
}

// Protocol of an API template. REST is implemented first; gRPC/WS are reserved
// so the protocol adapter can grow without a model change.
type Protocol string

const (
	ProtocolREST Protocol = "rest"
	ProtocolGRPC Protocol = "grpc"
	ProtocolWS   Protocol = "ws"
)

// Valid reports whether p is a known protocol.
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolREST, ProtocolGRPC, ProtocolWS:
		return true
	default:
		return false
	}
}

// CredentialStrategy selects how virtual users obtain auth credentials.
type CredentialStrategy string

const (
	// CredPool injects pre-supplied credentials (JWT/session/member info).
	CredPool CredentialStrategy = "pool"
	// CredBootstrapSignup registers accounts up front, one per virtual user.
	CredBootstrapSignup CredentialStrategy = "bootstrap-signup"
	// CredLogin mints a token at run time by walking a standalone login flow
	// (POST a login/token endpoint, capture the token from the response) and
	// hands it to a virtual user. It composes above the pool/bootstrap providers:
	// the login flow is referenced by LoginFlowID, never inlined as a node in the
	// main scenario graph, so the simulated traffic never observes the login.
	CredLogin CredentialStrategy = "login"
	// CredMint SELF-ISSUES a JWT per virtual user by signing one locally with a key
	// the operator holds — the M1 case (a service whose tokens are self-issued JWTs,
	// the operator controls the signing key). It SKIPS token acquisition entirely:
	// no login, refresh or capture, each VU gets a token instantly. It is keyed by
	// MintSpec on the pool. It does NOT help third-party/managed-IdP (Auth0/Cognito/
	// Firebase) or opaque tokens — only self-issued JWT, because you cannot sign for
	// a key you do not hold.
	CredMint CredentialStrategy = "mint"
	// CredExec runs an operator-supplied COMMAND per virtual user and uses its stdout
	// as the credential — the universal bring-your-own-token escape hatch for services
	// tmula cannot authenticate to declaratively (social/SDK login, third-party IdP
	// consent flows, exotic auth). It is keyed by ExecSpec on the pool. It is
	// SECURITY-SENSITIVE: it runs ARBITRARY local commands, so a run is rejected unless
	// the OPERATOR explicitly opts in (a CLI flag / env at run start) — merely having
	// strategy:"exec" in an untrusted scenario file MUST NOT run anything. The command
	// is run via argv (NEVER a shell), under a timeout and an output cap, and its egress
	// is NOT governed by safety.Guard — the operator owns that risk.
	CredExec CredentialStrategy = "exec"
)

// Valid reports whether s is a known credential strategy.
func (s CredentialStrategy) Valid() bool {
	switch s {
	case CredPool, CredBootstrapSignup, CredLogin, CredMint, CredExec:
		return true
	default:
		return false
	}
}

// LoginScope selects how many principals a login (CredLogin) pool mints. per-user
// (the default) runs the login flow once per virtual user so each authenticates
// as a distinct principal; shared runs it once and hands the single token to every
// session (the client_credentials machine-to-machine case).
type LoginScope string

const (
	// LoginPerUser mints one token per virtual user (the default).
	LoginPerUser LoginScope = "per-user"
	// LoginShared mints one token shared by every session (client_credentials).
	LoginShared LoginScope = "shared"
)

// Valid reports whether s is a known login scope. The empty value is NOT valid
// here (callers treat "" as the per-user default before validating); pass an
// explicit scope to Valid.
func (s LoginScope) Valid() bool {
	switch s {
	case LoginPerUser, LoginShared:
		return true
	default:
		return false
	}
}

// LoadStrategy shapes how load is applied to a target API over time.
type LoadStrategy string

const (
	LoadWeight LoadStrategy = "weight"
	LoadRamp   LoadStrategy = "ramp"
	LoadSpike  LoadStrategy = "spike"
	LoadSoak   LoadStrategy = "soak"
)

// Valid reports whether s is a known load strategy.
func (s LoadStrategy) Valid() bool {
	switch s {
	case LoadWeight, LoadRamp, LoadSpike, LoadSoak:
		return true
	default:
		return false
	}
}

// RunMode is the execution topology of a run.
type RunMode string

const (
	RunLocal       RunMode = "local"
	RunDistributed RunMode = "distributed"
)

// Valid reports whether m is a known run mode.
func (m RunMode) Valid() bool {
	switch m {
	case RunLocal, RunDistributed:
		return true
	default:
		return false
	}
}

// RunStatus is the lifecycle state of a run.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunKilled    RunStatus = "killed"
	RunFailed    RunStatus = "failed"
)

// Valid reports whether s is a known run status.
func (s RunStatus) Valid() bool {
	switch s {
	case RunPending, RunRunning, RunCompleted, RunKilled, RunFailed:
		return true
	default:
		return false
	}
}

// FindingCategory classifies a detected issue.
type FindingCategory string

const (
	// FindingThreshold: a metric crossed a configured threshold.
	FindingThreshold FindingCategory = "threshold"
	// FindingContract: response schema or status did not match expectations.
	FindingContract FindingCategory = "contract"
	// FindingMutation: a mutated/invalid input surfaced an error.
	FindingMutation FindingCategory = "mutation"
	// FindingAvailability: sustained timeouts/5xx indicate saturation or downtime.
	FindingAvailability FindingCategory = "availability"
)

// Valid reports whether c is a known finding category.
func (c FindingCategory) Valid() bool {
	switch c {
	case FindingThreshold, FindingContract, FindingMutation, FindingAvailability:
		return true
	default:
		return false
	}
}

// Severity ranks a finding.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool {
	switch s {
	case SeverityCritical, SeverityWarning, SeverityInfo:
		return true
	default:
		return false
	}
}

// AccessRole controls what a principal may do.
type AccessRole string

const (
	// RoleOperator has full control-plane access (local tool operator).
	RoleOperator AccessRole = "operator"
	// RoleViewer may read a shared report (holds a share token), nothing else.
	RoleViewer AccessRole = "viewer"
)

// Valid reports whether r is a known access role.
func (r AccessRole) Valid() bool {
	switch r {
	case RoleOperator, RoleViewer:
		return true
	default:
		return false
	}
}
