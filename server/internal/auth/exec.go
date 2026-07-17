package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// exec.go implements the CredExec strategy: run an operator-supplied COMMAND per
// virtual user and use its stdout as the credential — the universal bring-your-own-
// token escape hatch for services tmula cannot authenticate to declaratively (social/
// SDK login, third-party IdP consent flows, exotic auth).
//
// It is exposed as a TokenFunc (NewExecTokenFunc), so the CredExec strategy REUSES
// LoginProvider over it (see NewProvider): cache-by-index, in-flight dedup and Refresh
// all come for free, and Refresh simply re-runs the command (no refresh transport).
//
// SECURITY (the guardrails ARE the feature — see also domain.ExecSpec):
//   - The command is run via exec.CommandContext(ctx, argv[0], argv[1:]...) — NEVER a
//     shell, never `sh -c` — so a shell metacharacter in an argument is passed literally
//     and cannot inject a command.
//   - Per-invocation timeout (the context deadline) bounds a hung command; the stdout
//     read is capped so a runaway command cannot OOM the run.
//   - Operator secrets travel in Env (passed to the child explicitly), never in argv
//     (which is visible via `ps`).
//   - Empty token / non-zero exit / timeout / over-cap each surface a CLEAR error — a
//     virtual user is never authenticated as nobody.
//   - The command's stdout/token and the env VALUES are NEVER logged.
//
// EGRESS IS NOT GOVERNED BY safety.Guard: the command can talk to ANY host, bypassing
// the target allowlist + rate cap that bound the simulated traffic. The operator owns
// that risk; this is why an exec run requires an explicit operator opt-in at run start.

// NewExecTokenFunc returns a TokenFunc that, for each virtual user, renders
// {{.userIndex}} into the command's argv and env, runs the command (argv-only, no
// shell) under the spec's effective timeout, reads stdout up to the effective cap, and
// parses the credential: a JSON stdout is fed to load.DetectCredential (+ DetectRefresh)
// for token/subject/refresh, otherwise the trimmed stdout is the bare token. An empty
// token, a non-zero exit, a timeout, or output past the cap is a clear error.
func NewExecTokenFunc(spec domain.ExecSpec) TokenFunc {
	return func(ctx context.Context, userIndex int) (domain.Credential, error) {
		if len(spec.Command) == 0 || strings.TrimSpace(spec.Command[0]) == "" {
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: no command configured", userIndex)
		}

		// Render {{.userIndex}} into every argv element and env value so each virtual user
		// runs as a distinct principal. A template error fails loudly (a typo'd marker
		// must not silently run as user 0).
		data := map[string]string{"userIndex": strconv.Itoa(userIndex)}
		argv, err := renderExecArgs(spec.Command, data)
		if err != nil {
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: render command: %w", userIndex, err)
		}
		childEnv, err := renderExecEnv(spec.Env, data)
		if err != nil {
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: render env: %w", userIndex, err)
		}

		// Arm a per-invocation timeout so a hung command is killed rather than stalling
		// the run. CommandContext sends SIGKILL when the context is done.
		runCtx, cancel := context.WithTimeout(ctx, spec.EffectiveTimeout())
		defer cancel()

		// argv-ONLY: no shell. argv[0] is the program, the rest are its arguments, passed
		// literally — a metacharacter like ';' or '$(...)' in an arg is NOT interpreted.
		cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
		// Layer the operator's extra env over the parent's, so a value can reference host
		// env yet an explicit Env entry wins.
		if len(childEnv) > 0 {
			cmd.Env = append(os.Environ(), childEnv...)
		}

		// Read stdout through a LimitReader so a runaway command cannot balloon memory:
		// at most maxOut+1 bytes ever become resident, regardless of how much the child
		// writes (it blocks on its stdout write once the pipe fills, and the timeout/
		// cancel below kills it). The +1 is just enough to DETECT the overflow. Stderr is
		// captured separately so it is never mistaken for the token; it is never logged.
		maxOut := spec.EffectiveMaxOutputBytes()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: open stdout pipe: %w", userIndex, err)
		}
		if err := cmd.Start(); err != nil {
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: start command: %w", userIndex, err)
		}
		stdout, readErr := io.ReadAll(io.LimitReader(stdoutPipe, int64(maxOut)+1))
		overCap := len(stdout) > maxOut
		if overCap {
			// Stop trusting (and stop feeding) a flooding command: cancel kills the child so
			// Wait returns promptly instead of blocking on the child's now-full stdout pipe.
			cancel()
		}
		waitErr := cmd.Wait()
		// Over-cap wins: a flood is rejected before any exit/timeout classification (the
		// cancel above is what made Wait return). The command's stdout/stderr is NOT logged
		// (it may carry a token); only a short, non-sensitive shape note is surfaced.
		if overCap {
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: command output exceeded the %d-byte cap", userIndex, maxOut)
		}
		if readErr != nil {
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: read command output: %w", userIndex, readErr)
		}
		if waitErr != nil {
			// Distinguish a timeout from a plain non-zero exit for a clearer message.
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return domain.Credential{}, fmt.Errorf("auth: exec user %d: command timed out after %s", userIndex, spec.EffectiveTimeout())
			}
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				return domain.Credential{}, fmt.Errorf("auth: exec user %d: command exited %d", userIndex, exitErr.ExitCode())
			}
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: run command: %w", userIndex, waitErr)
		}

		token, subject, refresh, expiresIn := parseExecOutput(stdout)
		if token == "" {
			// Never authenticate as nobody: an empty token is a hard error.
			return domain.Credential{}, fmt.Errorf("auth: exec user %d: command produced no token on stdout", userIndex)
		}
		return domain.Credential{Subject: subject, Secret: token, Refresh: refresh, ExpiresIn: expiresIn}, nil
	}
}

// parseExecOutput turns the command's stdout into the credential fields. A JSON body is
// fed to the same detectors a login/signup response uses (load.DetectCredential pulls
// token+subject; DetectRefresh pulls the refresh token + lifetime). Anything else is
// treated as a bare token: the trimmed stdout. (DetectCredential already returns "" for
// non-JSON, so the bare-token fallback covers a plain-token command.)
func parseExecOutput(stdout []byte) (token, subject, refresh string, expiresIn time.Duration) {
	// The exec command speaks only over stdout, so there are no Set-Cookie headers to
	// consider — pass nil for the cookie fallback.
	token, subject, source := load.DetectCredentialSource(stdout, nil)
	if token != "" {
		// Name where the token was auto-captured from (a stdout JSON key), never the
		// value, so an exec run that prints a token response is as visible as a login.
		load.LogAutoCaptureSource(source)
		// A JSON body: also recover the OAuth2 refresh grant data for completeness, so a
		// command that prints a full token response feeds Refresh/expiry like a login does.
		refresh, expiresIn = load.DetectRefresh(stdout)
		return token, subject, refresh, expiresIn
	}
	// Not a recognizable JSON token body: treat the trimmed stdout as the bare token.
	return strings.TrimSpace(string(stdout)), "", "", 0
}

// renderExecArgs renders {{.userIndex}} into each argv element under the strict
// missingkey=error mode (a typo'd marker fails loudly), preserving order.
func renderExecArgs(args []string, data map[string]string) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		v, err := renderExecTemplate(fmt.Sprintf("arg %d", i), a, data)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// renderExecEnv renders {{.userIndex}} into each env value and returns "KEY=VALUE"
// strings (the form exec.Cmd.Env expects). The value is never logged.
func renderExecEnv(env map[string]string, data map[string]string) ([]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		rendered, err := renderExecTemplate("env "+k, v, data)
		if err != nil {
			return nil, err
		}
		out = append(out, k+"="+rendered)
	}
	return out, nil
}

// renderExecTemplate parses and executes one argv/env template under missingkey=error,
// the same strict mode the request and mint renderers use, so an unknown marker fails
// at run time rather than silently emitting "<no value>".
func renderExecTemplate(name, text string, data map[string]string) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("auth: exec %s template: %w", name, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("auth: exec %s template: %w", name, err)
	}
	return b.String(), nil
}
