package api

import (
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// bootstrapValidateSpec builds a minimal closed spec carrying a bootstrap pool.
func bootstrapValidateSpec(pool *domain.CredentialPool) RunSpec {
	spec := specFor("http://127.0.0.1:1", 1)
	spec.CredentialPool = pool
	return spec
}

// TestValidateAcceptsBootstrapWithTeardown is the rejection-lift: a bootstrap pool
// with a well-formed signup flow that DECLARES a teardown is accepted on the run
// path.
func TestValidateAcceptsBootstrapWithTeardown(t *testing.T) {
	spec := bootstrapValidateSpec(signupPool(true)) // with teardown
	if err := spec.Validate(); err != nil {
		t.Fatalf("bootstrap pool with a teardown flow should be accepted, got: %v", err)
	}
}

// TestValidateRejectsBootstrapWithoutTeardownOrKeep is the GATING SAFETY rule: a
// bootstrap pool with a signup flow but NO teardown is rejected by default — the
// only escape is --keep-accounts.
func TestValidateRejectsBootstrapWithoutTeardownOrKeep(t *testing.T) {
	noTeardown := signupPool(false) // signup flow, no teardown
	spec := bootstrapValidateSpec(noTeardown)
	err := spec.Validate()
	if err == nil {
		t.Fatal("a bootstrap pool with no teardown and no --keep-accounts must be rejected")
	}
	if !strings.Contains(err.Error(), "teardown") && !strings.Contains(err.Error(), "keep-accounts") {
		t.Errorf("rejection should mention teardown / keep-accounts, got: %v", err)
	}

	// --keep-accounts is the escape: the same no-teardown pool is then accepted.
	keep := signupPool(false)
	keep.KeepAccounts = true
	if err := bootstrapValidateSpec(keep).Validate(); err != nil {
		t.Errorf("a no-teardown bootstrap pool with --keep-accounts should be accepted, got: %v", err)
	}
}

// TestValidateRejectsBootstrapWithoutSignupFlow proves a bootstrap pool with only a
// legacy flow-id (no declarative SignupFlow) is not runnable on this path.
func TestValidateRejectsBootstrapWithoutSignupFlow(t *testing.T) {
	flow := domain.ID("signup")
	spec := bootstrapValidateSpec(&domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, BootstrapFlowID: &flow})
	if err := spec.Validate(); err == nil {
		t.Fatal("a bootstrap pool with no signup flow should be rejected on the run path")
	}
}

// TestValidateKeepsBootstrapWorkersRejected pins the load-bearing invariant: a
// bootstrap pool with distributed workers stays rejected (P3+P4), even with a
// teardown flow — distributed bootstrap is a follow-up.
func TestValidateKeepsBootstrapWorkersRejected(t *testing.T) {
	spec := bootstrapValidateSpec(signupPool(true))
	spec.Workers = []string{"127.0.0.1:65535"}
	if err := spec.Validate(); err == nil {
		t.Fatal("bootstrap + workers must stay rejected (distributed bootstrap is a follow-up)")
	}
}

// TestCreateExperimentAcceptsBootstrapWithTeardown confirms the Go-level submission
// path (the in-process CLI) accepts a runnable bootstrap pool.
func TestCreateExperimentAcceptsBootstrapWithTeardown(t *testing.T) {
	spec := specAuth("http://127.0.0.1:1", 1, signupPool(true))
	srv := NewServer(load.NewRESTAdapter(time.Second))
	if _, err := srv.CreateExperiment(spec); err != nil {
		t.Fatalf("CreateExperiment should accept a bootstrap pool with teardown: %v", err)
	}
}
