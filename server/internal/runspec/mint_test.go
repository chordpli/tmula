package runspec

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestCredentialProviderBuildsMintFromEnv resolves an HS256 mint key from an env var
// IN-PROCESS and builds a working provider whose Acquire returns a signed token with
// a per-index sub. The env key is read verbatim; nothing about it is serialized.
func TestCredentialProviderBuildsMintFromEnv(t *testing.T) {
	t.Setenv("TMULA_TEST_MINT_KEY", "the-symmetric-secret")
	pool := &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredMint,
		Mint: &domain.MintSpec{
			Alg:            domain.MintHS256,
			SecretEncoding: domain.MintEncodingRaw,
			Key:            &domain.CredentialSourceRef{Env: "TMULA_TEST_MINT_KEY"},
			Subject:        "u{{.userIndex}}",
			TTL:            time.Hour,
		},
	}
	spec := mintSpecFor(pool)
	prov, err := spec.CredentialProvider()
	if err != nil {
		t.Fatalf("CredentialProvider mint: %v", err)
	}
	if prov == nil {
		t.Fatal("mint pool produced a nil provider")
	}
	c0, err := prov.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatalf("Acquire(0): %v", err)
	}
	c1, err := prov.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("Acquire(1): %v", err)
	}
	if c0.Subject != "u0" || c1.Subject != "u1" {
		t.Errorf("subjects = %q,%q; want u0,u1 (per-index)", c0.Subject, c1.Subject)
	}
	if strings.Count(c0.Secret, ".") != 2 {
		t.Errorf("minted secret is not a compact JWS: %q", c0.Secret)
	}
}

// TestCredentialProviderMintMissingEnvErrors surfaces a clear error (not a silent
// anonymous run) when the mint key env var is unset.
func TestCredentialProviderMintMissingEnvErrors(t *testing.T) {
	pool := &domain.CredentialPool{
		Strategy: domain.CredMint,
		Mint: &domain.MintSpec{
			Alg: domain.MintHS256, SecretEncoding: domain.MintEncodingRaw,
			Key: &domain.CredentialSourceRef{Env: "TMULA_DEFINITELY_UNSET_MINT_KEY"}, TTL: time.Hour,
		},
	}
	spec := mintSpecFor(pool)
	if _, err := spec.CredentialProvider(); err == nil {
		t.Fatal("an unset mint key env var should error")
	}
}

// TestValidateMintWithWorkersAllowed pins the P4 change: distributed mint is now
// ALLOWED — the pool ships only its key REFERENCE (the resolved key is json:"-"),
// so each worker resolves the same key locally and self-issues per global index
// without a secret on the wire.
func TestValidateMintWithWorkersAllowed(t *testing.T) {
	pool := &domain.CredentialPool{
		Strategy: domain.CredMint,
		Mint: &domain.MintSpec{
			Alg: domain.MintHS256, SecretEncoding: domain.MintEncodingRaw,
			Key: &domain.CredentialSourceRef{Env: "K"}, TTL: time.Hour,
		},
	}
	spec := mintSpecFor(pool)
	spec.Workers = []string{"localhost:7000"}
	if err := spec.Validate(); err != nil {
		t.Fatalf("mint + distributed workers should be allowed (ships only the key reference): %v", err)
	}
}

// mintSpecFor builds a minimal runnable closed-model RunSpec with the given mint pool.
func mintSpecFor(pool *domain.CredentialPool) RunSpec {
	return RunSpec{
		Experiment: domain.Experiment{
			Name: "t", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: 1, DeviationRate: 0, AuthStrategy: domain.CredMint},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL: "http://localhost:9000", Allowlist: []string{"localhost"},
			RateCap: domain.RateCap{MaxRPS: 10, MaxConcurrency: 10}, EnvClass: domain.EnvDev,
		},
		Graph:          domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a"}}},
		Templates:      map[domain.ID]domain.APITemplate{},
		Start:          "a",
		MaxSteps:       1,
		UserCount:      1,
		Seed:           1,
		CredentialPool: pool,
	}
}
