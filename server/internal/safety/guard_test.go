package safety

import (
	"errors"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

func newTestGuard(t *testing.T) *Guard {
	t.Helper()
	g, err := NewGuard(Config{
		Allowlist:      []string{"localhost", "*.staging.internal"},
		MaxRPS:         2,
		MaxConcurrency: 2,
	})
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	return g
}

func TestAllowHost(t *testing.T) {
	g := newTestGuard(t)
	ok := []string{"http://localhost:8080/x", "https://api.staging.internal/v1"}
	for _, u := range ok {
		if err := g.AllowHost(u); err != nil {
			t.Errorf("AllowHost(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{"http://evil.com/x", "https://prod.internal/", "http://staging.internal"}
	for _, u := range bad {
		if err := g.AllowHost(u); err == nil {
			t.Errorf("AllowHost(%q) = nil, want error", u)
		}
	}
}

func TestRateCap(t *testing.T) {
	g := newTestGuard(t) // MaxRPS=2
	now := time.Unix(0, 0)
	g.setClock(func() time.Time { return now })

	if err := g.Reserve(); err != nil {
		t.Fatalf("reserve 1: %v", err)
	}
	g.Release()
	if err := g.Reserve(); err != nil {
		t.Fatalf("reserve 2: %v", err)
	}
	g.Release()
	// Tokens exhausted (2 used in the same instant).
	err := g.Reserve()
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != "rps" {
		t.Fatalf("reserve 3 should be rps limited, got %v", err)
	}
	// Advance a second: refill.
	now = now.Add(time.Second)
	if err := g.Reserve(); err != nil {
		t.Fatalf("reserve after refill: %v", err)
	}
}

func TestConcurrencyCap(t *testing.T) {
	g := newTestGuard(t) // MaxConcurrency=2
	now := time.Unix(0, 0)
	g.setClock(func() time.Time { return now })

	if err := g.Reserve(); err != nil {
		t.Fatalf("reserve 1: %v", err)
	}
	now = now.Add(time.Second) // refill tokens so we hit the concurrency cap, not rps
	if err := g.Reserve(); err != nil {
		t.Fatalf("reserve 2: %v", err)
	}
	now = now.Add(time.Second)
	err := g.Reserve()
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != "concurrency" {
		t.Fatalf("reserve 3 should be concurrency limited, got %v", err)
	}
	g.Release()
	now = now.Add(time.Second)
	if err := g.Reserve(); err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
}

func TestManualKillSwitch(t *testing.T) {
	g := newTestGuard(t)
	g.Kill("operator stop")
	if killed, reason := g.Killed(); !killed || reason != "operator stop" {
		t.Fatalf("Killed() = %v, %q", killed, reason)
	}
	if err := g.AllowHost("http://localhost/x"); err == nil {
		t.Error("AllowHost after kill should fail")
	}
	var ke *KilledError
	if err := g.Reserve(); !errors.As(err, &ke) {
		t.Errorf("Reserve after kill should be KilledError, got %v", err)
	}
}

func TestAutoKillTrips(t *testing.T) {
	g, _ := NewGuard(Config{
		Allowlist:      []string{"localhost"},
		MaxRPS:         100,
		MaxConcurrency: 100,
		AutoKill:       &AutoKill{ErrorRateThreshold: 0.5, MinSamples: 4},
	})
	// 1 ok then 3 errors => 4 samples, error rate 0.75 > 0.5.
	g.ReportOutcome(true)
	if killed, _ := g.Killed(); killed {
		t.Fatal("should not trip before MinSamples")
	}
	g.ReportOutcome(false)
	g.ReportOutcome(false)
	g.ReportOutcome(false)
	if killed, reason := g.Killed(); !killed {
		t.Fatalf("auto-kill should have tripped, reason=%q", reason)
	}
}

func TestAutoKillDisabledByDefault(t *testing.T) {
	g := newTestGuard(t) // no AutoKill
	for i := 0; i < 20; i++ {
		g.ReportOutcome(false)
	}
	if killed, _ := g.Killed(); killed {
		t.Fatal("auto-kill must be disabled by default (observe saturation)")
	}
}

func TestProdLockedRejected(t *testing.T) {
	env := domain.TargetEnv{
		BaseURL:   "https://prod.internal",
		Allowlist: []string{"prod.internal"},
		RateCap:   domain.RateCap{MaxRPS: 10, MaxConcurrency: 10},
		EnvClass:  domain.EnvProdLocked,
	}
	if _, err := NewGuardForEnv(env, nil, false); err == nil {
		t.Fatal("prod-locked env must be refused without explicit unlock")
	}
	if _, err := NewGuardForEnv(env, nil, true); err != nil {
		t.Fatalf("prod-locked env with explicit unlock should build: %v", err)
	}
}
