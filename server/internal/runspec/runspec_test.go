package runspec_test

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// minimalSpec returns the smallest valid closed RunSpec pointing at the given
// base URL. It is the baseline that individual guard tests mutate.
func minimalSpec(baseURL string) runspec.RunSpec {
	return runspec.RunSpec{
		Experiment: domain.Experiment{
			Name:            "guard-test",
			TargetEnvID:     "e",
			ScenarioGraphID: "g",
			Params: domain.ExperimentParams{
				VirtualUserCount: 1,
				AuthStrategy:     domain.CredPool,
			},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL:   baseURL,
			Allowlist: []string{"127.0.0.1"},
			RateCap:   domain.RateCap{MaxRPS: 100, MaxConcurrency: 10},
			EnvClass:  domain.EnvDev,
		},
		Graph: domain.ScenarioGraph{
			ID:    "g",
			Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}},
		},
		Templates: map[domain.ID]domain.APITemplate{
			"ta": {Method: "GET", Path: "/a"},
		},
		Start:     "a",
		MaxSteps:  1,
		UserCount: 1,
		Seed:      1,
	}
}

func twoEntryPool() *domain.CredentialPool {
	return &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredPool,
		Entries: []domain.Credential{
			{Subject: "u0", Secret: "tok-0"},
			{Subject: "u1", Secret: "tok-1"},
		},
	}
}

// TestValidateCredentialPoolInvariant pins the invariant that is load-bearing
// for reproduce fidelity: a spec combining a credential pool with distributed
// workers (Workers > 0 or AggregateWorkers) must be rejected by Validate.
//
// This invariant is what guarantees distributed runs are always unauthenticated,
// so CredentialProvider returns (nil, nil) for them and the reproduce path
// (sessionUser in reproduce.go) keeps the replayed session user-consistent.
// If this test breaks, the reproduce fidelity guarantee breaks silently too.
func TestValidateCredentialPoolInvariant(t *testing.T) {
	base := func() runspec.RunSpec { return minimalSpec("http://127.0.0.1:1") }

	t.Run("credential pool without workers is valid", func(t *testing.T) {
		s := base()
		s.CredentialPool = twoEntryPool()
		if err := s.Validate(); err != nil {
			t.Errorf("valid pool without workers rejected: %v", err)
		}
	})

	t.Run("credential pool with Workers is rejected", func(t *testing.T) {
		s := base()
		s.CredentialPool = twoEntryPool()
		s.Workers = []string{"127.0.0.1:65535"}
		err := s.Validate()
		if err == nil {
			t.Fatal("credential pool + Workers must be rejected (load-bearing for reproduce fidelity)")
		}
		// The rejection message must explain the reason so the operator
		// understands why and can distinguish it from other validation errors.
		if !strings.Contains(err.Error(), "worker") {
			t.Errorf("rejection message should mention workers, got: %v", err)
		}
	})

	t.Run("credential pool with AggregateWorkers is rejected", func(t *testing.T) {
		s := base()
		s.CredentialPool = twoEntryPool()
		s.AggregateWorkers = true
		err := s.Validate()
		if err == nil {
			t.Fatal("credential pool + AggregateWorkers must be rejected (load-bearing for reproduce fidelity)")
		}
		if !strings.Contains(err.Error(), "worker") {
			t.Errorf("rejection message should mention workers, got: %v", err)
		}
	})

	t.Run("credential pool with Workers and AggregateWorkers is rejected", func(t *testing.T) {
		s := base()
		s.CredentialPool = twoEntryPool()
		s.Workers = []string{"127.0.0.1:65535"}
		s.AggregateWorkers = true
		err := s.Validate()
		if err == nil {
			t.Fatal("credential pool + Workers + AggregateWorkers must be rejected (load-bearing for reproduce fidelity)")
		}
	})

	t.Run("nil credential pool with Workers is valid", func(t *testing.T) {
		s := base()
		s.CredentialPool = nil
		s.Workers = []string{"127.0.0.1:65535"}
		// Workers alone (no pool) is a valid distributed unauthenticated run.
		// Validation may still fail for other reasons (e.g. open model check)
		// but NOT because of the credential pool invariant.
		err := s.Validate()
		if err != nil && strings.Contains(err.Error(), "credential pool") {
			t.Errorf("nil pool + Workers should not fail on credential pool check, got: %v", err)
		}
	})
}
