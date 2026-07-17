package domain

import (
	"testing"
	"time"
)

// TestExecSpecValidate pins the shape checks: a non-empty argv with a non-empty
// program (argv[0]) is required; a negative timeout / output cap is rejected; a sane
// command is accepted (a zero Timeout/MaxOutputBytes is allowed — the provider fills
// the default and the hard max).
func TestExecSpecValidate(t *testing.T) {
	cases := []struct {
		name string
		spec ExecSpec
		ok   bool
	}{
		{"ok", ExecSpec{Command: []string{"/usr/local/bin/get-token", "--user", "{{.userIndex}}"}, Timeout: 5 * time.Second}, true},
		{"ok-zero-timeout-defaults", ExecSpec{Command: []string{"/bin/echo", "tok"}}, true},
		{"empty-command", ExecSpec{}, false},
		{"empty-program", ExecSpec{Command: []string{"", "arg"}}, false},
		{"blank-program", ExecSpec{Command: []string{"   ", "arg"}}, false},
		{"negative-timeout", ExecSpec{Command: []string{"/bin/echo"}, Timeout: -time.Second}, false},
		{"negative-cap", ExecSpec{Command: []string{"/bin/echo"}, MaxOutputBytes: -1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.spec.Validate()
			if c.ok && err != nil {
				t.Fatalf("Validate() = %v, want ok", err)
			}
			if !c.ok && err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
}

// TestCredentialPoolExecValidate pins the pool-level wiring: a CredExec pool needs an
// exec block; a pool that carries one and is otherwise well-formed validates.
func TestCredentialPoolExecValidate(t *testing.T) {
	good := CredentialPool{
		ID:       "p",
		Strategy: CredExec,
		Exec:     &ExecSpec{Command: []string{"/bin/echo", "tok"}, Timeout: time.Second},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("a well-formed exec pool should validate: %v", err)
	}
	missing := CredentialPool{ID: "p", Strategy: CredExec}
	if err := missing.Validate(); err == nil {
		t.Fatal("an exec pool with no exec block should error")
	}
}

// TestCredExecStrategyValid keeps the new strategy in the known set.
func TestCredExecStrategyValid(t *testing.T) {
	if !CredExec.Valid() {
		t.Fatal("CredExec should be a valid credential strategy")
	}
	if CredExec != "exec" {
		t.Errorf("CredExec = %q, want exec", CredExec)
	}
}

// TestExecSpecResolvedHardMaxAndDefault checks the provider-facing accessors that fold
// in the default timeout and clamp the output cap to the hard max, so a hung or
// runaway command cannot stall or OOM the run regardless of what the spec asked for.
func TestExecSpecResolvedHardMaxAndDefault(t *testing.T) {
	// Zero timeout defaults to a sane non-zero value.
	if d := (ExecSpec{}).EffectiveTimeout(); d <= 0 {
		t.Fatalf("EffectiveTimeout default = %s, want > 0", d)
	}
	// A wildly large timeout is clamped to the hard max.
	clamped := ExecSpec{Timeout: 24 * time.Hour}.EffectiveTimeout()
	if clamped >= 24*time.Hour {
		t.Fatalf("EffectiveTimeout did not clamp a huge timeout: %s", clamped)
	}
	// Zero cap defaults to a sane non-zero value.
	if c := (ExecSpec{}).EffectiveMaxOutputBytes(); c <= 0 {
		t.Fatalf("EffectiveMaxOutputBytes default = %d, want > 0", c)
	}
	// A wildly large cap is clamped to the hard max.
	clampedCap := ExecSpec{MaxOutputBytes: 1 << 40}.EffectiveMaxOutputBytes()
	if clampedCap >= 1<<40 {
		t.Fatalf("EffectiveMaxOutputBytes did not clamp a huge cap: %d", clampedCap)
	}
}
