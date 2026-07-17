package scenariofile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

const mintHSYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  strategy: mint
  mint:
    alg: HS256
    secretEncoding: base64
    key:
      env: TMULA_MINT_SECRET
    subject: "user-{{.userIndex}}"
    claims:
      role: tester
      tenant: acme
    ttl: 1h
`

// TestExpandAuthMint threads a compact mint auth block into the RunSpec: the strategy
// is CredMint, the pool carries a MintSpec with the alg, the (non-secret) key
// reference, the subject template, the claims and the TTL. The resulting spec
// validates, and the key reference is the only key material on the pool — the secret
// is resolved in-process at provider-build time, never serialized.
func TestExpandAuthMint(t *testing.T) {
	s, err := Parse([]byte(mintHSYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || spec.CredentialPool.Strategy != domain.CredMint {
		t.Fatalf("expected a mint credential pool, got %+v", spec.CredentialPool)
	}
	m := spec.CredentialPool.Mint
	if m == nil {
		t.Fatal("mint pool carries no MintSpec")
	}
	if m.Alg != domain.MintHS256 {
		t.Errorf("alg = %q, want HS256", m.Alg)
	}
	if m.SecretEncoding != domain.MintEncodingBase64 {
		t.Errorf("secretEncoding = %q, want base64", m.SecretEncoding)
	}
	if m.Key == nil || m.Key.Env != "TMULA_MINT_SECRET" {
		t.Errorf("key ref = %+v, want env TMULA_MINT_SECRET", m.Key)
	}
	if m.Subject != "user-{{.userIndex}}" {
		t.Errorf("subject = %q", m.Subject)
	}
	if m.Claims["role"] != "tester" || m.Claims["tenant"] != "acme" {
		t.Errorf("claims = %+v", m.Claims)
	}
	if m.TTL.String() != "1h0m0s" {
		t.Errorf("ttl = %s, want 1h", m.TTL)
	}
	if spec.Experiment.Params.AuthStrategy != domain.CredMint {
		t.Errorf("experiment auth strategy = %q, want mint", spec.Experiment.Params.AuthStrategy)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded mint spec failed validation: %v", err)
	}
}

// TestExpandAuthMintRSPEM accepts an RS256 mint authored with a PEM key reference.
func TestExpandAuthMintRSPEM(t *testing.T) {
	y := `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
auth:
  strategy: mint
  mint:
    alg: RS256
    key:
      file: signing-key.pem
    subject: "u{{.userIndex}}"
    ttl: 30m
`
	s, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool.Mint.Alg != domain.MintRS256 {
		t.Errorf("alg = %q, want RS256", spec.CredentialPool.Mint.Alg)
	}
	if spec.CredentialPool.Mint.Key.File != "signing-key.pem" {
		t.Errorf("key file = %q", spec.CredentialPool.Mint.Key.File)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("RS256 mint spec failed validation: %v", err)
	}
}

// TestExpandAuthMintRejects pins the validation rejections at the scenariofile layer:
// a bad alg, a missing key, an HS encoding that is not raw/base64/base64url, and a
// non-positive ttl are all refused with a clear message.
func TestExpandAuthMintRejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"bad-alg", `
target: http://localhost:9000
flow: [{id: a, request: GET /a}]
auth:
  strategy: mint
  mint:
    alg: HS512
    key: {env: K}
    ttl: 1h
`},
		{"missing-key", `
target: http://localhost:9000
flow: [{id: a, request: GET /a}]
auth:
  strategy: mint
  mint:
    alg: HS256
    secretEncoding: raw
    ttl: 1h
`},
		{"bad-encoding", `
target: http://localhost:9000
flow: [{id: a, request: GET /a}]
auth:
  strategy: mint
  mint:
    alg: HS256
    secretEncoding: hex
    key: {env: K}
    ttl: 1h
`},
		{"ttl-zero", `
target: http://localhost:9000
flow: [{id: a, request: GET /a}]
auth:
  strategy: mint
  mint:
    alg: HS256
    secretEncoding: raw
    key: {env: K}
    ttl: 0s
`},
		{"no-mint-block", `
target: http://localhost:9000
flow: [{id: a, request: GET /a}]
auth:
  strategy: mint
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if _, err := Expand(s); err == nil {
				t.Errorf("expected %s to be rejected", tc.name)
			}
		})
	}
}

// TestExpandAuthMintMarshalHidesKey is the AD-011 contract: a mint pool serializes
// only the non-secret key REFERENCE, never any resolved secret bytes.
func TestExpandAuthMintMarshalHidesKey(t *testing.T) {
	s, err := Parse([]byte(mintHSYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	b, err := json.Marshal(spec.CredentialPool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "resolvedKey") {
		t.Errorf("serialized mint pool leaked a resolved key: %s", b)
	}
	// The non-secret reference round-trips.
	if !strings.Contains(string(b), "TMULA_MINT_SECRET") {
		t.Errorf("serialized mint pool dropped its key reference: %s", b)
	}
}
