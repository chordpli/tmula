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

// TestValidateRejectsUnresolvedSource pins the D1 contract: a credential pool
// that still carries a Source (not yet resolved into Entries) is rejected by the
// run path. The CLI resolves a source into entries at expand time, so a spec
// reaching a run must carry real entries; an unresolved source on the wire would
// mean the server was asked to read a client-chosen path, which it must not do.
func TestValidateRejectsUnresolvedSource(t *testing.T) {
	s := minimalSpec("http://127.0.0.1:1")
	s.CredentialPool = &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredPool,
		Source:   &domain.CredentialSourceRef{File: "creds.csv", Format: "csv"},
	}
	err := s.Validate()
	if err == nil {
		t.Fatal("a pool carrying an unresolved source must be rejected by the run path")
	}
	if !strings.Contains(err.Error(), "resolved") {
		t.Errorf("rejection should explain the source must be resolved, got: %v", err)
	}

	// A resolved pool (entries, no source) is still accepted.
	ok := minimalSpec("http://127.0.0.1:1")
	ok.CredentialPool = twoEntryPool()
	if err := ok.Validate(); err != nil {
		t.Errorf("a resolved entries pool must still validate: %v", err)
	}
}

// loginSpec returns a minimal valid CredLogin spec: a login pool plus the
// standalone login flow it mints from.
func loginSpec(baseURL string) runspec.RunSpec {
	s := minimalSpec(baseURL)
	flowID := domain.ID("login")
	s.CredentialPool = &domain.CredentialPool{ID: "p", Strategy: domain.CredLogin, LoginFlowID: &flowID}
	s.LoginFlow = &runspec.LoginFlowSpec{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "tlogin"}}},
		Templates: map[domain.ID]domain.APITemplate{"tlogin": {Method: "POST", Path: "/login", Extract: map[string]string{"token": "access_token"}}},
		Start:     "login",
		TokenVar:  "token",
	}
	s.Experiment.Params.AuthStrategy = domain.CredLogin
	return s
}

// TestValidateLoginPool pins the CredLogin run-path guards: a well-formed login
// pool (with its login flow) validates; a login pool with no login flow is
// rejected; and — invariant 8 — a login pool combined with distributed workers is
// rejected exactly like a static pool, because a minted token is still a secret the
// worker fan-out cannot resolve.
func TestValidateLoginPool(t *testing.T) {
	// A well-formed login spec validates.
	if err := loginSpec("http://127.0.0.1:1").Validate(); err != nil {
		t.Errorf("a well-formed login spec was rejected: %v", err)
	}

	// A login pool with no login flow is rejected.
	noFlow := loginSpec("http://127.0.0.1:1")
	noFlow.LoginFlow = nil
	if err := noFlow.Validate(); err == nil {
		t.Error("a login pool with no login flow must be rejected")
	}

	// A login pool with a malformed login flow (no token capture) is rejected.
	badFlow := loginSpec("http://127.0.0.1:1")
	badFlow.LoginFlow.TokenVar = ""
	if err := badFlow.Validate(); err == nil {
		t.Error("a login flow with no token capture var must be rejected")
	}

	// Invariant 8: login + workers is rejected (a minted token is still a secret).
	dist := loginSpec("http://127.0.0.1:1")
	dist.Workers = []string{"127.0.0.1:65535"}
	if err := dist.Validate(); err == nil {
		t.Error("a login pool with distributed workers must be rejected (the minted token is a secret the workers cannot resolve)")
	}

	// Invariant 8: login + aggregate workers is rejected too.
	agg := loginSpec("http://127.0.0.1:1")
	agg.Workers = []string{"127.0.0.1:65535"}
	agg.AggregateWorkers = true
	if err := agg.Validate(); err == nil {
		t.Error("a login pool with aggregate workers must be rejected")
	}
}

// TestLoginProviderBuiltAboveRunspec pins that runspec does not try to build a
// login provider itself (it cannot — the transport lives above this leaf):
// CredentialProvider errors for a login pool rather than silently returning an
// unauthenticated (nil) provider, so a wiring bug fails loudly.
func TestLoginProviderBuiltAboveRunspec(t *testing.T) {
	s := loginSpec("http://127.0.0.1:1")
	if _, err := s.CredentialProvider(); err == nil {
		t.Error("runspec.CredentialProvider must not build a login provider (the orchestrator does)")
	}
}

// sourcePool returns a CredPool that references an external source (no inline
// entries), the reference-only shape distributed auth carries across the wire.
func sourcePool(ref *domain.CredentialSourceRef) *domain.CredentialPool {
	return &domain.CredentialPool{ID: "p", Strategy: domain.CredPool, Source: ref}
}

// bootstrapPool returns a runnable bootstrap-signup pool: a well-formed signup
// flow that provisions an account and a teardown that deprovisions it. It is
// runnable in-process, so a rejection with workers proves the WORKERS gate fires
// (not a missing-flow gate).
func bootstrapPool() *domain.CredentialPool {
	return &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredBootstrapSignup,
		SignupFlow: &domain.SignupFlow{
			Steps: []domain.SignupStep{{
				ID: "register", Method: "POST", Path: "/signup",
				Extract: map[string]string{"token": "accessToken", "uid": "id"},
			}},
			Start:         "register",
			Capture:       domain.SignupCapture{Token: "token", Subject: "uid"},
			Teardown:      []domain.SignupStep{{ID: "remove", Method: "DELETE", Path: "/accounts/{{.subject}}"}},
			TeardownStart: "remove",
		},
	}
}

// TestValidateDistributedSourceAuth pins the P3 D1 reconciliation — the run
// path's new split of the old blanket "pool + workers is rejected" rule:
//
//   - a file/env SOURCE pool WITH workers is ALLOWED (the distributed carve-out:
//     each worker resolves the shared, index-deterministic reference locally; no
//     secret crosses the wire);
//   - an INLINE-entries pool WITH workers stays REJECTED (secrets would serialize
//     into the wire spec);
//   - a SOURCE pool with NO workers stays REJECTED (the in-process/API server must
//     not read a client-chosen path off the wire; the CLI resolves single-node
//     sources at scenariofile.Expand);
//   - a BOOTSTRAP pool with workers stays REJECTED (P4 depends on this);
//   - an OPEN run with a source pool and workers stays REJECTED (open keys by
//     arrival index, not the closed pool index, so a source carve-out would key the
//     wrong principal — an explicit pin so it can never silently change).
func TestValidateDistributedSourceAuth(t *testing.T) {
	workers := func(s *runspec.RunSpec) { s.Workers = []string{"127.0.0.1:65535"} }

	t.Run("file source + workers is accepted", func(t *testing.T) {
		s := minimalSpec("http://127.0.0.1:1")
		s.CredentialPool = sourcePool(&domain.CredentialSourceRef{File: "creds.csv", Format: "csv"})
		workers(&s)
		if err := s.Validate(); err != nil {
			t.Fatalf("a file source pool with workers must be accepted (distributed carve-out), got: %v", err)
		}
	})

	t.Run("env source + workers is accepted", func(t *testing.T) {
		s := minimalSpec("http://127.0.0.1:1")
		s.CredentialPool = sourcePool(&domain.CredentialSourceRef{Env: "TMULA_TOKENS", Format: "tokens"})
		workers(&s)
		if err := s.Validate(); err != nil {
			t.Fatalf("an env source pool with workers must be accepted (distributed carve-out), got: %v", err)
		}
	})

	t.Run("source + workers + AggregateWorkers is accepted", func(t *testing.T) {
		s := minimalSpec("http://127.0.0.1:1")
		s.CredentialPool = sourcePool(&domain.CredentialSourceRef{File: "creds.csv", Format: "csv"})
		s.Workers = []string{"127.0.0.1:65535"}
		s.AggregateWorkers = true
		if err := s.Validate(); err != nil {
			t.Fatalf("a source pool with aggregate workers must be accepted, got: %v", err)
		}
	})

	t.Run("inline entries + workers stays rejected", func(t *testing.T) {
		s := minimalSpec("http://127.0.0.1:1")
		s.CredentialPool = twoEntryPool()
		workers(&s)
		if err := s.Validate(); err == nil {
			t.Fatal("inline entries + workers must stay rejected (secrets would cross the wire)")
		}
	})

	t.Run("source + no workers stays rejected", func(t *testing.T) {
		s := minimalSpec("http://127.0.0.1:1")
		s.CredentialPool = sourcePool(&domain.CredentialSourceRef{File: "creds.csv", Format: "csv"})
		err := s.Validate()
		if err == nil {
			t.Fatal("a source pool without workers must stay rejected (the server must not read a client path)")
		}
		if !strings.Contains(err.Error(), "resolved") {
			t.Errorf("rejection should explain the source must be resolved (single-node), got: %v", err)
		}
	})

	t.Run("bootstrap + workers stays rejected", func(t *testing.T) {
		s := minimalSpec("http://127.0.0.1:1")
		s.CredentialPool = bootstrapPool()
		s.Experiment.Params.AuthStrategy = domain.CredBootstrapSignup
		workers(&s)
		if err := s.Validate(); err == nil {
			t.Fatal("bootstrap-signup + workers must stay rejected (P4 depends on this)")
		}
	})

	t.Run("open + source + workers stays rejected", func(t *testing.T) {
		s := minimalSpec("http://127.0.0.1:1")
		s.Workload = &domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, StartRate: 1},
			DurationSeconds: 1,
		}
		s.CredentialPool = sourcePool(&domain.CredentialSourceRef{File: "creds.csv", Format: "csv"})
		workers(&s)
		if err := s.Validate(); err == nil {
			t.Fatal("open + source + workers must stay rejected (open keys by arrival index, not pool index)")
		}
	})
}
