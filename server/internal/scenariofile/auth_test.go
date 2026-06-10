package scenariofile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
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
