package safety

import "testing"

// TestAutoKillRollingWindow proves the fix: a long healthy run followed by a
// recent burst of errors trips the auto-kill, even though the *cumulative*
// error rate stays tiny. A cumulative implementation would never trip here.
func TestAutoKillRollingWindow(t *testing.T) {
	g, err := NewGuard(Config{
		Allowlist:      []string{"x"},
		MaxRPS:         100,
		MaxConcurrency: 100,
		AutoKill:       &AutoKill{ErrorRateThreshold: 0.5, MinSamples: 10},
	})
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	for i := 0; i < 100; i++ {
		g.ReportOutcome(true) // long healthy stretch
	}
	if killed, _ := g.Killed(); killed {
		t.Fatal("a healthy run must not trip auto-kill")
	}
	// 6 of the last 10 outcomes are errors => rolling rate 0.6 > 0.5.
	// Cumulative rate would be 6/106 ≈ 0.06 and would never trip.
	for i := 0; i < 6; i++ {
		g.ReportOutcome(false)
	}
	if killed, reason := g.Killed(); !killed {
		t.Fatalf("rolling auto-kill should trip on a recent error spike; reason=%q", reason)
	}
}

func TestAllowHostBareHostAndPort(t *testing.T) {
	g, _ := NewGuard(Config{Allowlist: []string{"localhost"}, MaxRPS: 1, MaxConcurrency: 1})
	for _, target := range []string{"localhost", "localhost:8080", "http://localhost/x"} {
		if err := g.AllowHost(target); err != nil {
			t.Errorf("AllowHost(%q) = %v, want nil", target, err)
		}
	}
	if err := g.AllowHost("evil.com:443"); err == nil {
		t.Error("bare host not in allowlist should be rejected")
	}
}
