package runspec

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestCredentialProviderBuildsExec builds an exec provider at the leaf (exec needs no
// flow/transport, like mint): CredentialProvider returns a working provider whose
// Acquire runs the command per index. The command re-execs the test binary as a token
// printer (see auth/exec_test.go's TestMain helper), so this is fully self-contained
// and OS-portable.
func TestCredentialProviderBuildsExec(t *testing.T) {
	pool := &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredExec,
		Exec: &domain.ExecSpec{
			Command: append([]string{os.Args[0]}, "x"),
			Env:     map[string]string{"TMULA_EXEC_HELPER": "bare", "TMULA_EXEC_IDX": "{{.userIndex}}"},
			Timeout: 5 * time.Second,
		},
	}
	spec := execSpecFor(pool)
	prov, err := spec.CredentialProvider()
	if err != nil {
		t.Fatalf("CredentialProvider exec: %v", err)
	}
	if prov == nil {
		t.Fatal("exec pool produced a nil provider")
	}
	c0, err := prov.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatalf("Acquire(0): %v", err)
	}
	c1, err := prov.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("Acquire(1): %v", err)
	}
	if c0.Secret == c1.Secret {
		t.Errorf("exec did not distinguish per-VU: %q == %q", c0.Secret, c1.Secret)
	}
	if !strings.Contains(c0.Secret, "tok-0-") {
		t.Errorf("userIndex 0 not rendered into token: %q", c0.Secret)
	}
}

// TestValidateExecWithWorkersRejected keeps exec + distributed workers refused: the
// exec output is a json:"-" secret workers cannot resolve, and arbitrary command
// execution is never fanned out across remote workers.
func TestValidateExecWithWorkersRejected(t *testing.T) {
	pool := &domain.CredentialPool{
		Strategy: domain.CredExec,
		Exec:     &domain.ExecSpec{Command: []string{"/bin/echo", "tok"}, Timeout: time.Second},
	}
	spec := execSpecFor(pool)
	spec.Workers = []string{"localhost:7000"}
	if err := spec.Validate(); err == nil {
		t.Fatal("exec + distributed workers should be rejected")
	}
}

// TestValidateExecAggregateWorkersRejected pins the same rejection under the
// aggregate-workers flag (the other half of hasWorkers).
func TestValidateExecAggregateWorkersRejected(t *testing.T) {
	pool := &domain.CredentialPool{
		Strategy: domain.CredExec,
		Exec:     &domain.ExecSpec{Command: []string{"/bin/echo", "tok"}, Timeout: time.Second},
	}
	spec := execSpecFor(pool)
	spec.AggregateWorkers = true
	spec.Workers = []string{"localhost:7000"}
	if err := spec.Validate(); err == nil {
		t.Fatal("exec + aggregate workers should be rejected")
	}
}

// TestValidateExecLocalAccepted confirms a local (no-workers) exec run passes the run
// path validation, so the gate is the operator opt-in (StartRun), not the spec shape.
func TestValidateExecLocalAccepted(t *testing.T) {
	pool := &domain.CredentialPool{
		Strategy: domain.CredExec,
		Exec:     &domain.ExecSpec{Command: []string{"/bin/echo", "tok"}, Timeout: time.Second},
	}
	spec := execSpecFor(pool)
	if err := spec.Validate(); err != nil {
		t.Fatalf("a local exec run should validate: %v", err)
	}
}

// execSpecFor builds a minimal runnable closed-model RunSpec with the given exec pool.
func execSpecFor(pool *domain.CredentialPool) RunSpec {
	return RunSpec{
		Experiment: domain.Experiment{
			Name: "t", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: 1, DeviationRate: 0, AuthStrategy: domain.CredExec},
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
