package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// exec.go defines ExecSpec, the config for the CredExec strategy: run an operator-
// supplied COMMAND per virtual user and use its stdout as the credential. It is the
// universal bring-your-own-token escape hatch for services tmula cannot authenticate
// to declaratively. It is transport-free domain data — the auth.NewExecTokenFunc
// layer runs the command. Unlike the minted Credential (whose Secret/Refresh are
// json:"-"), an ExecSpec's Env VALUES are serialized and round-trip (they are not
// redacted), so an INLINE secret in Env is as exposed as any other spec field — prefer
// a host-env reference (an Env value that resolves from the operator's environment on
// the run host) so the secret never enters the document or the wire.
//
// SECURITY POSTURE (these constraints are the feature):
//   - The command is run via ARGV (Command[0] is the program, the rest are its args),
//     NEVER through a shell, so a shell metacharacter in an arg is passed literally and
//     cannot inject a command.
//   - Secrets belong in Env, not Command: argv is visible in process listings (`ps`),
//     so a token passed as an argument would leak to any local user. Env is passed to
//     the child explicitly.
//   - A per-invocation Timeout and an output cap (MaxOutputBytes) bound a hung or
//     runaway command so it cannot stall or OOM the run. Both have a sane default and a
//     hard max enforced by EffectiveTimeout / EffectiveMaxOutputBytes.
//   - The command's egress is NOT governed by safety.Guard: it can reach ANY host,
//     bypassing the target allowlist and rate cap. The operator owns that risk.
type ExecSpec struct {
	// Command is the ARGV to run: Command[0] is the program, the remaining elements are
	// its arguments. It is NOT a shell string — there is no shell, so no word splitting,
	// globbing or operator interpretation. Argv elements may template {{.userIndex}} so
	// each virtual user runs as a distinct principal. Required (non-empty, with a
	// non-empty program).
	Command []string `json:"command"`
	// Env is extra environment passed to the child process, layered over the parent's
	// environment. Values may reference {{.userIndex}} (rendered per virtual user) and,
	// being plain env, may carry operator secrets — keep secrets HERE, never in Command
	// (argv is visible via `ps`). Optional.
	Env map[string]string `json:"env,omitempty"`
	// Timeout bounds ONE invocation of the command. Zero takes the default
	// (DefaultExecTimeout); any value is clamped to MaxExecTimeout. A hung command is
	// killed at the timeout and surfaces a clear error (never authenticate as nobody).
	Timeout time.Duration `json:"timeout,omitempty"`
	// MaxOutputBytes caps how much stdout is read before the command is treated as a
	// runaway and errored. Zero takes the default (DefaultExecMaxOutputBytes); any value
	// is clamped to MaxExecMaxOutputBytes. It bounds memory so a flooding command cannot
	// OOM the run.
	MaxOutputBytes int `json:"maxOutputBytes,omitempty"`
}

// MarshalJSON / UnmarshalJSON route Timeout through flexDuration, so an exec spec
// serializes its timeout as a human string ("30s") and accepts either a string
// (browser- or hand-authored) or a nanosecond number (a Go-marshaled spec) on decode —
// without this a web-posted exec spec 400s on the string timeout the console sends.
func (e ExecSpec) MarshalJSON() ([]byte, error) {
	type alias ExecSpec
	return json.Marshal(&struct {
		Timeout flexDuration `json:"timeout,omitempty"`
		*alias
	}{flexDuration(e.Timeout), (*alias)(&e)})
}

func (e *ExecSpec) UnmarshalJSON(data []byte) error {
	type alias ExecSpec
	aux := &struct {
		Timeout flexDuration `json:"timeout,omitempty"`
		*alias
	}{alias: (*alias)(e)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	e.Timeout = time.Duration(aux.Timeout)
	return nil
}

const (
	// DefaultExecTimeout is the per-invocation timeout when ExecSpec.Timeout is zero.
	DefaultExecTimeout = 30 * time.Second
	// MaxExecTimeout is the hard ceiling a configured timeout is clamped to, so even a
	// misconfigured spec cannot let a hung command stall the run indefinitely.
	MaxExecTimeout = 5 * time.Minute
	// DefaultExecMaxOutputBytes is the stdout cap when ExecSpec.MaxOutputBytes is zero
	// (64 KiB — generous for a token or a small JSON body).
	DefaultExecMaxOutputBytes = 64 * 1024
	// MaxExecMaxOutputBytes is the hard ceiling a configured cap is clamped to (1 MiB),
	// so even a misconfigured spec cannot let a runaway command balloon memory.
	MaxExecMaxOutputBytes = 1024 * 1024
)

// Validate checks the exec spec is runnable: a non-empty argv with a non-empty program
// (Command[0]), and a non-negative timeout / output cap. It validates SHAPE only — it
// does not run, resolve or inspect the command; that (and the operator opt-in gate) is
// the run path's job. A zero Timeout / MaxOutputBytes is fine (the Effective* accessors
// fill the default), so a minimal exec block stays minimal.
func (e ExecSpec) Validate() error {
	if len(e.Command) == 0 {
		return fmt.Errorf("exec: a command (argv) is required — argv[0] is the program, the rest are its arguments")
	}
	if strings.TrimSpace(e.Command[0]) == "" {
		return fmt.Errorf("exec: the command's program (argv[0]) must not be empty")
	}
	if e.Timeout < 0 {
		return fmt.Errorf("exec: timeout must not be negative")
	}
	if e.MaxOutputBytes < 0 {
		return fmt.Errorf("exec: maxOutputBytes must not be negative")
	}
	return nil
}

// EffectiveTimeout resolves the per-invocation timeout: the configured value, or
// DefaultExecTimeout when zero, clamped to MaxExecTimeout. It is the value the provider
// arms the context with, so a hung command is always bounded.
func (e ExecSpec) EffectiveTimeout() time.Duration {
	t := e.Timeout
	if t <= 0 {
		t = DefaultExecTimeout
	}
	if t > MaxExecTimeout {
		t = MaxExecTimeout
	}
	return t
}

// EffectiveMaxOutputBytes resolves the stdout cap: the configured value, or
// DefaultExecMaxOutputBytes when zero, clamped to MaxExecMaxOutputBytes. It is the
// ceiling the provider reads stdout up to, so a runaway command is always bounded.
func (e ExecSpec) EffectiveMaxOutputBytes() int {
	n := e.MaxOutputBytes
	if n <= 0 {
		n = DefaultExecMaxOutputBytes
	}
	if n > MaxExecMaxOutputBytes {
		n = MaxExecMaxOutputBytes
	}
	return n
}
