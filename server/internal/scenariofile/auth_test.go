package scenariofile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/runspec"
)

const authYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  users:
    - subject: alice
      token: tok-alice
    - subject: bob
      token: tok-bob
`

// TestExpandAuthPool threads a compact auth block into the RunSpec's credential
// pool: the strategy defaults to "pool", each user's token maps to the domain
// credential's (masked) secret, and the resulting spec validates.
func TestExpandAuthPool(t *testing.T) {
	s, err := Parse([]byte(authYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil {
		t.Fatal("expanded spec has no credential pool")
	}
	pool := spec.CredentialPool
	if pool.Strategy != domain.CredPool {
		t.Errorf("strategy = %q, want %q (default)", pool.Strategy, domain.CredPool)
	}
	if len(pool.Entries) != 2 {
		t.Fatalf("pool entries = %d, want 2", len(pool.Entries))
	}
	if pool.Entries[0].Subject != "alice" || pool.Entries[0].Secret != "tok-alice" {
		t.Errorf("entry[0] = %+v, want alice/tok-alice", pool.Entries[0])
	}
	if pool.Entries[1].Subject != "bob" || pool.Entries[1].Secret != "tok-bob" {
		t.Errorf("entry[1] = %+v, want bob/tok-bob", pool.Entries[1])
	}
	// The experiment's recorded strategy follows the pool.
	if spec.Experiment.Params.AuthStrategy != domain.CredPool {
		t.Errorf("experiment auth strategy = %q, want %q", spec.Experiment.Params.AuthStrategy, domain.CredPool)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded authenticated spec failed validation: %v", err)
	}
}

const loginAuthYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  strategy: login
  login:
    flow:
      - id: login
        request: POST /login
        body: '{"u":"svc"}'
        extract:
          token: access_token
          subject: user
    capture:
      token: token
      subject: subject
`

// TestExpandAuthLogin threads a compact login auth block into the RunSpec: the
// strategy is CredLogin, the pool references a login flow, and the spec carries the
// compiled login flow (graph + templates + captures) for the orchestrator to mint
// tokens from. The resulting spec validates.
func TestExpandAuthLogin(t *testing.T) {
	s, err := Parse([]byte(loginAuthYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || spec.CredentialPool.Strategy != domain.CredLogin {
		t.Fatalf("expected a login credential pool, got %+v", spec.CredentialPool)
	}
	if spec.CredentialPool.LoginFlowID == nil || *spec.CredentialPool.LoginFlowID == "" {
		t.Errorf("login pool has no login flow id")
	}
	if spec.LoginFlow == nil {
		t.Fatal("expanded login spec carries no login flow")
	}
	if spec.LoginFlow.TokenVar != "token" {
		t.Errorf("login flow token var = %q, want token", spec.LoginFlow.TokenVar)
	}
	if spec.LoginFlow.SubjectVar != "subject" {
		t.Errorf("login flow subject var = %q, want subject", spec.LoginFlow.SubjectVar)
	}
	if spec.LoginFlow.Start == "" {
		t.Error("login flow has no start node")
	}
	// The login flow's template must carry the POST /login request and the captures.
	if len(spec.LoginFlow.Templates) == 0 {
		t.Fatal("login flow has no templates")
	}
	if spec.Experiment.Params.AuthStrategy != domain.CredLogin {
		t.Errorf("experiment auth strategy = %q, want login", spec.Experiment.Params.AuthStrategy)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded login spec failed validation: %v", err)
	}
}

// loginAuthAutoYAML is a login auth block with NO capture: an empty token capture
// means tmula auto-detects the token from the login response.
const loginAuthAutoYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  strategy: login
  login:
    flow:
      - id: login
        request: POST /login
        body: '{"u":"svc"}'
`

// TestExpandAuthLoginAutoDetect accepts a login block with no explicit capture: the
// expanded spec carries an empty TokenVar (auto-detect) and still validates.
func TestExpandAuthLoginAutoDetect(t *testing.T) {
	s, err := Parse([]byte(loginAuthAutoYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.LoginFlow == nil {
		t.Fatal("expanded login spec carries no login flow")
	}
	if spec.LoginFlow.TokenVar != "" {
		t.Errorf("login flow token var = %q, want empty (auto-detect)", spec.LoginFlow.TokenVar)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("login spec with auto-detect capture failed validation: %v", err)
	}
}

// TestExpandAuthLoginScope reads the optional scope and defaults it to per-user.
func TestExpandAuthLoginScope(t *testing.T) {
	shared := strings.Replace(loginAuthYAML, "    capture:", "    scope: shared\n    capture:", 1)
	s, err := Parse([]byte(shared))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool.LoginScope != domain.LoginShared {
		t.Errorf("scope = %q, want shared", spec.CredentialPool.LoginScope)
	}
}

// TestExpandAuthLoginRoundTrip pins the scenariofile round-trip: a login auth block
// parses, expands, and the resulting spec marshals WITHOUT leaking any secret
// (there is none to leak — the token is minted at run time), and re-parses.
func TestExpandAuthLoginRoundTrip(t *testing.T) {
	s, err := Parse([]byte(loginAuthYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Re-marshal the parsed scenario and parse it again: the authoring block is
	// stable across a YAML/JSON round-trip.
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal scenario: %v", err)
	}
	s2, err := Parse(b)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	spec, err := Expand(s2)
	if err != nil {
		t.Fatalf("expand re-parsed: %v", err)
	}
	if spec.CredentialPool.Strategy != domain.CredLogin {
		t.Fatalf("round-tripped strategy = %q, want login", spec.CredentialPool.Strategy)
	}
	// The expanded spec marshals with no secret-shaped field (the login mints at run
	// time; nothing secret is authored in a login block).
	sb, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if strings.Contains(string(sb), "\"-\"") {
		t.Errorf("spec leaked a masked field literally: %s", sb)
	}
}

// TestExpandAuthPoolMarshalHidesSecret confirms the secret authored in the file
// reaches the in-memory pool but never serializes out of the expanded spec, so an
// authenticated scenario still honors the at-rest masking guarantee.
func TestExpandAuthPoolMarshalHidesSecret(t *testing.T) {
	s, err := Parse([]byte(authYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// In memory the secret is present (so the runtime can authenticate)...
	if spec.CredentialPool.Entries[0].Secret != "tok-alice" {
		t.Fatal("secret missing from the in-memory pool")
	}
	// ...but it must never appear in a serialized spec.
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, secret := range []string{"tok-alice", "tok-bob"} {
		if strings.Contains(out, secret) {
			t.Errorf("serialized spec leaked secret %q", secret)
		}
	}
	if !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Errorf("serialized spec dropped the non-sensitive subjects: %s", out)
	}
}

// TestExpandAuthRejects covers the auth-block guards: an unknown strategy, a
// bootstrap-signup request (a follow-up not wired on this path), and a "pool"
// strategy with no users are all rejected.
func TestExpandAuthRejects(t *testing.T) {
	base := func() Scenario {
		return Scenario{Target: "http://h:1", Flow: []Step{{ID: "a", Request: "GET /a"}}}
	}

	unknown := base()
	unknown.Auth = &Auth{Strategy: "oauth2", Users: []Credential{{Subject: "a", Token: "t"}}}
	if _, err := Expand(unknown); err == nil {
		t.Error("unknown auth strategy should be rejected")
	}

	boot := base()
	boot.Auth = &Auth{Strategy: string(domain.CredBootstrapSignup)}
	if _, err := Expand(boot); err == nil {
		t.Error("bootstrap-signup auth strategy should be rejected on this path")
	}

	empty := base()
	empty.Auth = &Auth{Strategy: "pool"}
	if _, err := Expand(empty); err == nil {
		t.Error("a pool strategy with no users should be rejected")
	}
}

// TestExpandAuthFileSource resolves a file-backed auth source into the same
// Entries a literal users block would produce, leaving the pool Source nil so the
// resolved spec carries real credentials. The FileSource root is the scenario
// file's directory, passed to ExpandFrom.
func TestExpandAuthFileSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "creds.csv"), []byte("subject,token\nalice,tok-alice\nbob,tok-bob\n"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	const fileYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
auth:
  source:
    file: creds.csv
    format: csv
`
	s, err := Parse([]byte(fileYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := ExpandFrom(s, dir)
	if err != nil {
		t.Fatalf("ExpandFrom: %v", err)
	}
	if spec.CredentialPool == nil {
		t.Fatal("expanded spec has no credential pool")
	}
	pool := spec.CredentialPool
	if pool.Source != nil {
		t.Errorf("resolved pool should carry nil Source, got %+v", pool.Source)
	}
	if pool.Strategy != domain.CredPool {
		t.Errorf("strategy = %q, want %q", pool.Strategy, domain.CredPool)
	}
	if len(pool.Entries) != 2 {
		t.Fatalf("pool entries = %d, want 2", len(pool.Entries))
	}
	if pool.Entries[0].Subject != "alice" || pool.Entries[0].Secret != "tok-alice" {
		t.Errorf("entry[0] = %+v, want alice/tok-alice", pool.Entries[0])
	}
	if pool.Entries[1].Subject != "bob" || pool.Entries[1].Secret != "tok-bob" {
		t.Errorf("entry[1] = %+v, want bob/tok-bob", pool.Entries[1])
	}
	// The resolved pool matches what an equivalent literal users block produces.
	if err := spec.Validate(); err != nil {
		t.Errorf("resolved authenticated spec failed validation: %v", err)
	}
	// The secret never serializes out of the resolved spec.
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "tok-alice") || strings.Contains(string(b), "tok-bob") {
		t.Errorf("resolved spec leaked a secret: %s", b)
	}
}

// TestExpandRefKeepsSourceUnresolved pins the distributed-engine seam: ExpandRef
// carries an external auth source as an unresolved reference-only SourceRef (file
// + format, never a secret) and reads NO file — so the reference can cross to a
// remote engine whose workers resolve it. The credential file deliberately does
// not exist; ExpandRef must still succeed.
func TestExpandRefKeepsSourceUnresolved(t *testing.T) {
	dir := t.TempDir() // creds.csv intentionally absent
	const fileYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
auth:
  source:
    file: creds.csv
    format: csv
`
	s, err := Parse([]byte(fileYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := ExpandRef(s, dir)
	if err != nil {
		t.Fatalf("ExpandRef must not read the file: %v", err)
	}
	if spec.CredentialPool == nil || spec.CredentialPool.Source == nil {
		t.Fatal("ExpandRef must carry an unresolved source reference")
	}
	if spec.CredentialPool.Source.File != "creds.csv" || spec.CredentialPool.Source.Format != "csv" {
		t.Errorf("source ref = %+v, want file creds.csv/csv", spec.CredentialPool.Source)
	}
	if len(spec.CredentialPool.Entries) != 0 {
		t.Errorf("ExpandRef must not load entries, got %d", len(spec.CredentialPool.Entries))
	}
	// ExpandFrom (the single-node path) still resolves — and here that fails
	// because the file is absent, proving the two paths differ as intended.
	if _, err := ExpandFrom(s, dir); err == nil {
		t.Error("ExpandFrom must still resolve (and fail on the missing file)")
	}
}

// TestExpandAuthSourceWrapAroundDeterminism is the critic's reproduce-determinism
// guard: a file source with N rows, driven by a pool provider over K>N users,
// hands out exactly entries[i%N] — identical to the equivalent inline users
// block. It asserts the assignment for i<N AND i>=N, so the wrap-around (the
// part reproduce relies on) is pinned, not just the first lap.
func TestExpandAuthSourceWrapAroundDeterminism(t *testing.T) {
	const csv = "subject,token\nu0,tok-0\nu1,tok-1\nu2,tok-2\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "creds.csv"), []byte(csv), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	const sourceYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
auth:
  source:
    file: creds.csv
    format: csv
`
	const inlineYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
auth:
  users:
    - subject: u0
      token: tok-0
    - subject: u1
      token: tok-1
    - subject: u2
      token: tok-2
`
	sourceSpec := mustExpandFrom(t, sourceYAML, dir)
	inlineSpec := mustExpandFrom(t, inlineYAML, dir)

	srcProv, err := auth.NewProvider(*sourceSpec.CredentialPool, auth.ProviderDeps{})
	if err != nil {
		t.Fatalf("source provider: %v", err)
	}
	inProv, err := auth.NewProvider(*inlineSpec.CredentialPool, auth.ProviderDeps{})
	if err != nil {
		t.Fatalf("inline provider: %v", err)
	}

	const n = 3
	// Drive K = 2N+1 users so the comparison crosses two wrap boundaries.
	for i := 0; i < 2*n+1; i++ {
		sc, err := srcProv.Acquire(context.Background(), i)
		if err != nil {
			t.Fatalf("source Acquire(%d): %v", i, err)
		}
		ic, err := inProv.Acquire(context.Background(), i)
		if err != nil {
			t.Fatalf("inline Acquire(%d): %v", i, err)
		}
		// Identical to the inline pool...
		if sc != ic {
			t.Errorf("Acquire(%d): source=%+v inline=%+v differ", i, sc, ic)
		}
		// ...and exactly entries[i%N], for i<N and i>=N alike.
		wantSubject := []string{"u0", "u1", "u2"}[i%n]
		wantSecret := []string{"tok-0", "tok-1", "tok-2"}[i%n]
		if sc.Subject != wantSubject || sc.Secret != wantSecret {
			t.Errorf("Acquire(%d) = %+v, want entries[%d%%%d] = %s/%s", i, sc, i, n, wantSubject, wantSecret)
		}
	}
}

// mustExpandFrom parses and expands a scenario document, failing the test on any
// error. It returns the expanded spec.
func mustExpandFrom(t *testing.T, doc, dir string) runspec.RunSpec {
	t.Helper()
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := ExpandFrom(s, dir)
	if err != nil {
		t.Fatalf("ExpandFrom: %v", err)
	}
	if spec.CredentialPool == nil {
		t.Fatal("expanded spec has no credential pool")
	}
	return spec
}

// TestExpandAuthEnvSource resolves an env-backed auth source into Entries.
func TestExpandAuthEnvSource(t *testing.T) {
	t.Setenv("TMULA_TEST_AUTH", "tok-x\ntok-y\n")
	const envYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
auth:
  source:
    env: TMULA_TEST_AUTH
    format: tokens
`
	s, err := Parse([]byte(envYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || spec.CredentialPool.Source != nil {
		t.Fatalf("env source did not resolve to entries: %+v", spec.CredentialPool)
	}
	if len(spec.CredentialPool.Entries) != 2 {
		t.Fatalf("env entries = %d, want 2", len(spec.CredentialPool.Entries))
	}
	if spec.CredentialPool.Entries[0].Secret != "tok-x" || spec.CredentialPool.Entries[1].Secret != "tok-y" {
		t.Errorf("env entries = %+v, want tok-x/tok-y", spec.CredentialPool.Entries)
	}
}

// TestExpandAuthSourceMissingFile errors at expand time when the referenced file
// does not exist.
func TestExpandAuthSourceMissingFile(t *testing.T) {
	dir := t.TempDir()
	const missingYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
auth:
  source:
    file: nope.csv
    format: csv
`
	s, err := Parse([]byte(missingYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := ExpandFrom(s, dir); err == nil {
		t.Error("a missing source file should error at expand time")
	}
}

// TestExpandAuthRejectsUsersAndSource rejects an auth block that sets both inline
// users and an external source.
func TestExpandAuthRejectsUsersAndSource(t *testing.T) {
	const bothYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
auth:
  users:
    - subject: alice
      token: tok
  source:
    env: TMULA_TEST_AUTH
    format: tokens
`
	s, err := Parse([]byte(bothYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := Expand(s); err == nil {
		t.Error("an auth block with both users and a source should be rejected")
	}
}

// TestExpandAuthFromJSON confirms the auth block parses from JSON too (the same
// parser handles YAML and JSON), so a JSON scenario can authenticate.
func TestExpandAuthFromJSON(t *testing.T) {
	const j = `{"target":"http://h:1","flow":[{"id":"a","request":"GET /a"}],` +
		`"auth":{"users":[{"subject":"alice","token":"tok"}]}}`
	s, err := Parse([]byte(j))
	if err != nil {
		t.Fatalf("Parse json: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || len(spec.CredentialPool.Entries) != 1 {
		t.Fatalf("credential pool = %+v, want one entry", spec.CredentialPool)
	}
	if spec.CredentialPool.Entries[0].Secret != "tok" {
		t.Errorf("entry secret = %q, want tok", spec.CredentialPool.Entries[0].Secret)
	}
}
