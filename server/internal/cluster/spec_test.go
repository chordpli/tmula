package cluster

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// baseValidSpec is a minimal ShardSpec that passes Validate, used as the trunk
// the credential-source tests vary a single field on.
func baseValidSpec() ShardSpec {
	return ShardSpec{
		Graph: domain.ScenarioGraph{
			ID:    "g1",
			Nodes: []domain.Node{{ID: "n1", APITemplateID: "t1"}},
		},
		Templates:     map[domain.ID]domain.APITemplate{"t1": {ID: "t1", Protocol: domain.ProtocolREST, Method: "GET", Path: "/one"}},
		TargetBaseURL: "http://sut.local",
		Start:         "n1",
		MaxSteps:      5,
		Seed:          1,
	}
}

// TestShardSpecCredentialSourceRoundTrip pins that a reference-only credential
// source travels through encodeSpec/decodeSpec byte-faithfully — the file/env
// reference and its format, never a secret (a CredentialSourceRef carries none) —
// and that an absent source omits the field entirely so a source-less spec
// serializes byte-identically to before the field existed.
func TestShardSpecCredentialSourceRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("file source round-trips", func(t *testing.T) {
		t.Parallel()
		s := baseValidSpec()
		s.CredentialSource = &domain.CredentialSourceRef{File: "creds.csv", Format: "csv"}

		js, err := encodeSpec(s)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		got, err := decodeSpec(js)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.CredentialSource == nil {
			t.Fatal("credential source dropped on the wire")
		}
		if got.CredentialSource.File != "creds.csv" || got.CredentialSource.Format != "csv" {
			t.Fatalf("credential source not faithful: %+v", got.CredentialSource)
		}
	})

	t.Run("absent source omits the field", func(t *testing.T) {
		t.Parallel()
		js, err := encodeSpec(baseValidSpec())
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if strings.Contains(js, "credentialSource") {
			t.Errorf("a source-less spec must omit credentialSource, got: %s", js)
		}
	})
}

// TestShardSpecValidateCredentialSource pins that ShardSpec.Validate accepts a
// well-formed pool reference (file or env) and rejects a malformed one. A
// CredentialSourceRef is structurally pool-only (file/env/format, no secret and
// no bootstrap flow), so a bootstrap-signup pool can never produce a ShardSpec
// source: the only thing a worker may resolve off the wire is a shared, ordered
// pool reference. Validating the ref's shape here keeps anything else out.
func TestShardSpecValidateCredentialSource(t *testing.T) {
	t.Parallel()

	t.Run("valid file source accepted", func(t *testing.T) {
		t.Parallel()
		s := baseValidSpec()
		s.CredentialSource = &domain.CredentialSourceRef{File: "creds.csv", Format: "csv"}
		if err := s.Validate(); err != nil {
			t.Fatalf("a well-formed file source must validate, got: %v", err)
		}
	})

	t.Run("valid env source accepted", func(t *testing.T) {
		t.Parallel()
		s := baseValidSpec()
		s.CredentialSource = &domain.CredentialSourceRef{Env: "TMULA_TOKENS", Format: "tokens"}
		if err := s.Validate(); err != nil {
			t.Fatalf("a well-formed env source must validate, got: %v", err)
		}
	})

	t.Run("both file and env rejected", func(t *testing.T) {
		t.Parallel()
		s := baseValidSpec()
		s.CredentialSource = &domain.CredentialSourceRef{File: "creds.csv", Env: "TMULA_TOKENS", Format: "csv"}
		if err := s.Validate(); err == nil {
			t.Fatal("a source naming both file and env must be rejected")
		}
	})

	t.Run("unknown format rejected", func(t *testing.T) {
		t.Parallel()
		s := baseValidSpec()
		s.CredentialSource = &domain.CredentialSourceRef{File: "creds.csv", Format: "yaml"}
		if err := s.Validate(); err == nil {
			t.Fatal("a source with an unknown format must be rejected")
		}
	})

	t.Run("empty reference rejected", func(t *testing.T) {
		t.Parallel()
		s := baseValidSpec()
		s.CredentialSource = &domain.CredentialSourceRef{Format: "csv"}
		if err := s.Validate(); err == nil {
			t.Fatal("a source naming neither file nor env must be rejected")
		}
	})
}
