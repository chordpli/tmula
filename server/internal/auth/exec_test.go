package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestMain lets the test binary re-exec itself as a tiny "token printer" helper, so
// the exec TokenFunc can run a REAL child process with no shell and no external
// dependency — fully portable across darwin/linux CI. When TMULA_EXEC_HELPER is set
// the process behaves as the helper (per the mode the env selects) and exits;
// otherwise it runs the test suite normally.
func TestMain(m *testing.M) {
	switch os.Getenv("TMULA_EXEC_HELPER") {
	case "":
		os.Exit(m.Run())
	case "bare":
		// Print a bare token that embeds the per-VU index and any arg/env passed, so a
		// test can assert {{.userIndex}} differs per VU and that argv/env reached the
		// child verbatim (no shell interpretation).
		fmt.Printf("tok-%s-%s-%s\n", os.Getenv("TMULA_EXEC_IDX"), strings.Join(os.Args[1:], "|"), os.Getenv("TMULA_EXEC_EXTRA"))
		os.Exit(0)
	case "json":
		// Print a login-shaped JSON body the detectors recover token/subject/refresh from.
		fmt.Printf(`{"access_token":"jtok-%s","refresh_token":"r-%s","expires_in":900,"username":"sub-%s"}`,
			os.Getenv("TMULA_EXEC_IDX"), os.Getenv("TMULA_EXEC_IDX"), os.Getenv("TMULA_EXEC_IDX"))
		os.Exit(0)
	case "empty":
		// Exit 0 but print nothing — an empty token must be a clear error.
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	case "hang":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "flood":
		// Emit far more than any sane cap so the output-cap guard trips.
		big := strings.Repeat("x", 1<<20)
		fmt.Print(big)
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode")
		os.Exit(2)
	}
}

// helperSpec builds an ExecSpec whose command re-execs THIS test binary as the named
// helper mode, templating {{.userIndex}} into an env var the helper echoes back.
func helperSpec(mode string, extraArgs ...string) domain.ExecSpec {
	argv := append([]string{os.Args[0]}, extraArgs...)
	return domain.ExecSpec{
		Command: argv,
		Env: map[string]string{
			"TMULA_EXEC_HELPER": mode,
			"TMULA_EXEC_IDX":    "{{.userIndex}}",
		},
		Timeout: 5 * time.Second,
	}
}

// TestExecTokenFuncBareStdout treats trimmed stdout as the bare token and proves
// {{.userIndex}} renders differently per virtual user.
func TestExecTokenFuncBareStdout(t *testing.T) {
	fn := NewExecTokenFunc(helperSpec("bare"))
	c0, err := fn(context.Background(), 0)
	if err != nil {
		t.Fatalf("exec user 0: %v", err)
	}
	c7, err := fn(context.Background(), 7)
	if err != nil {
		t.Fatalf("exec user 7: %v", err)
	}
	if c0.Secret == "" || c7.Secret == "" {
		t.Fatalf("empty secrets: %q %q", c0.Secret, c7.Secret)
	}
	if c0.Secret == c7.Secret {
		t.Fatalf("expected {{.userIndex}} to differ per VU, got identical %q", c0.Secret)
	}
	if !strings.Contains(c0.Secret, "tok-0-") || !strings.Contains(c7.Secret, "tok-7-") {
		t.Fatalf("userIndex not rendered into output: %q %q", c0.Secret, c7.Secret)
	}
}

// TestExecTokenFuncJSONStdout parses a JSON stdout via the detectors, pulling token,
// subject and refresh.
func TestExecTokenFuncJSONStdout(t *testing.T) {
	fn := NewExecTokenFunc(helperSpec("json"))
	c, err := fn(context.Background(), 4)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if c.Secret != "jtok-4" {
		t.Errorf("token = %q, want jtok-4", c.Secret)
	}
	if c.Subject != "sub-4" {
		t.Errorf("subject = %q, want sub-4", c.Subject)
	}
	if c.Refresh != "r-4" {
		t.Errorf("refresh = %q, want r-4", c.Refresh)
	}
	if c.ExpiresIn != 900*time.Second {
		t.Errorf("expiresIn = %s, want 15m", c.ExpiresIn)
	}
}

// TestExecTokenFuncNoShell proves the command runs via argv, not a shell: a shell
// metacharacter in an argument is passed LITERALLY to the child (echoed back), never
// interpreted as a command separator.
func TestExecTokenFuncNoShell(t *testing.T) {
	// "; rm -rf /" would be catastrophic under a shell; as an argv element the helper
	// just echoes it back inside the token.
	fn := NewExecTokenFunc(helperSpec("bare", "a;b", "$(whoami)"))
	c, err := fn(context.Background(), 1)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(c.Secret, "a;b") || !strings.Contains(c.Secret, "$(whoami)") {
		t.Fatalf("metachars were not passed literally (shell interpretation?): %q", c.Secret)
	}
}

// TestExecTokenFuncEmpty errors when the command prints nothing (never authenticate
// as nobody).
func TestExecTokenFuncEmpty(t *testing.T) {
	fn := NewExecTokenFunc(helperSpec("empty"))
	if _, err := fn(context.Background(), 0); err == nil {
		t.Fatal("empty output should error")
	}
}

// TestExecTokenFuncNonZeroExit errors when the command exits non-zero.
func TestExecTokenFuncNonZeroExit(t *testing.T) {
	fn := NewExecTokenFunc(helperSpec("fail"))
	if _, err := fn(context.Background(), 0); err == nil {
		t.Fatal("non-zero exit should error")
	}
}

// TestExecTokenFuncTimeout errors when the command runs longer than the timeout.
func TestExecTokenFuncTimeout(t *testing.T) {
	spec := helperSpec("hang")
	spec.Timeout = 100 * time.Millisecond
	fn := NewExecTokenFunc(spec)
	start := time.Now()
	if _, err := fn(context.Background(), 0); err == nil {
		t.Fatal("a hung command should time out with an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout did not fire promptly (took %s)", elapsed)
	}
}

// TestExecTokenFuncOutputCap errors when the command floods stdout past the cap, so a
// runaway command cannot OOM the run.
func TestExecTokenFuncOutputCap(t *testing.T) {
	spec := helperSpec("flood")
	spec.MaxOutputBytes = 4096
	fn := NewExecTokenFunc(spec)
	if _, err := fn(context.Background(), 0); err == nil {
		t.Fatal("output over the cap should error")
	}
}

// TestNewExecTokenFuncEmptyCommand errors at build/run time when no command is set.
func TestExecTokenFuncEmptyCommand(t *testing.T) {
	fn := NewExecTokenFunc(domain.ExecSpec{})
	if _, err := fn(context.Background(), 0); err == nil {
		t.Fatal("an empty command should error")
	}
}

// TestNewProviderBuildsExec wires CredExec through NewProvider: it builds a
// LoginProvider over an exec-backed TokenFunc, so cache/dedup/refresh come for free.
// Acquire is cached+deterministic per index (one run per index); Refresh re-runs the
// command (no refresh transport is wired for exec).
func TestNewProviderBuildsExec(t *testing.T) {
	pool := domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredExec,
		Exec:     &domain.ExecSpec{Command: append([]string{os.Args[0]}, "z"), Env: map[string]string{"TMULA_EXEC_HELPER": "bare", "TMULA_EXEC_IDX": "{{.userIndex}}"}, Timeout: 5 * time.Second},
	}
	prov, err := NewProvider(pool, ProviderDeps{})
	if err != nil {
		t.Fatalf("NewProvider exec: %v", err)
	}
	lp, ok := prov.(*LoginProvider)
	if !ok {
		t.Fatalf("exec provider is %T, want *LoginProvider", prov)
	}
	a, err := lp.Acquire(context.Background(), 2)
	if err != nil {
		t.Fatalf("Acquire(2): %v", err)
	}
	b, err := lp.Acquire(context.Background(), 2)
	if err != nil {
		t.Fatalf("Acquire(2) cached: %v", err)
	}
	if a.Secret != b.Secret {
		t.Errorf("Acquire(2) not cached/deterministic: %q != %q", a.Secret, b.Secret)
	}
	if !strings.Contains(a.Secret, "tok-2-") {
		t.Errorf("userIndex 2 not rendered: %q", a.Secret)
	}
	// A different index is a distinct principal.
	c3, err := lp.Acquire(context.Background(), 3)
	if err != nil {
		t.Fatalf("Acquire(3): %v", err)
	}
	if c3.Secret == a.Secret {
		t.Errorf("index 2 and 3 should differ: %q", a.Secret)
	}
	// Refresh re-runs the command (the exec fallback path), yielding a fresh token for
	// the same principal.
	r, err := lp.Refresh(context.Background(), 2)
	if err != nil {
		t.Fatalf("Refresh(2): %v", err)
	}
	if !strings.Contains(r.Secret, "tok-2-") {
		t.Errorf("refresh did not re-run the command for index 2: %q", r.Secret)
	}
}

// TestNewProviderExecRequiresSpec refuses to build an exec provider with no spec (a
// wiring bug, not a silent anonymous run).
func TestNewProviderExecRequiresSpec(t *testing.T) {
	pool := domain.CredentialPool{ID: "p", Strategy: domain.CredExec}
	if _, err := NewProvider(pool, ProviderDeps{}); err == nil {
		t.Fatal("exec strategy with no exec spec should error")
	}
}
