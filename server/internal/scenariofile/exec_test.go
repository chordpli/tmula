package scenariofile

import (
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

const execYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  strategy: exec
  exec:
    command:
      - /usr/local/bin/get-token
      - --user
      - "{{.userIndex}}"
    env:
      ID_TOKEN_AUDIENCE: my-api
      USER_INDEX: "{{.userIndex}}"
    timeout: 10s
    maxOutputBytes: 65536
`

// TestExpandAuthExec threads a compact exec auth block into the RunSpec: the strategy
// is CredExec, the pool carries an ExecSpec with the argv command, the extra env, the
// timeout and the output cap. It carries no secret — operator secrets go in env values
// (which may reference host env), never serialized as part of the run.
func TestExpandAuthExec(t *testing.T) {
	s, err := Parse([]byte(execYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || spec.CredentialPool.Strategy != domain.CredExec {
		t.Fatalf("expected an exec credential pool, got %+v", spec.CredentialPool)
	}
	e := spec.CredentialPool.Exec
	if e == nil {
		t.Fatal("exec pool carries no ExecSpec")
	}
	if len(e.Command) != 3 || e.Command[0] != "/usr/local/bin/get-token" || e.Command[2] != "{{.userIndex}}" {
		t.Errorf("command = %v, want the argv triple", e.Command)
	}
	if e.Env["ID_TOKEN_AUDIENCE"] != "my-api" || e.Env["USER_INDEX"] != "{{.userIndex}}" {
		t.Errorf("env = %+v", e.Env)
	}
	if e.Timeout != 10*time.Second {
		t.Errorf("timeout = %s, want 10s", e.Timeout)
	}
	if e.MaxOutputBytes != 65536 {
		t.Errorf("maxOutputBytes = %d, want 65536", e.MaxOutputBytes)
	}
	if spec.Experiment.Params.AuthStrategy != domain.CredExec {
		t.Errorf("experiment auth strategy = %q, want exec", spec.Experiment.Params.AuthStrategy)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded exec spec failed validation: %v", err)
	}
}

// TestExpandAuthExecRejects rejects a malformed exec block at expand time with a clear,
// scenariofile-prefixed message rather than deferring to the run path.
func TestExpandAuthExecRejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "no-command",
			yaml: "target: http://x\nflow:\n  - id: a\n    request: GET /a\nauth:\n  strategy: exec\n  exec:\n    timeout: 5s\n",
			want: "command",
		},
		{
			name: "missing-exec-block",
			yaml: "target: http://x\nflow:\n  - id: a\n    request: GET /a\nauth:\n  strategy: exec\n",
			want: "exec",
		},
		{
			name: "bad-timeout",
			yaml: "target: http://x\nflow:\n  - id: a\n    request: GET /a\nauth:\n  strategy: exec\n  exec:\n    command: [/bin/echo, tok]\n    timeout: not-a-duration\n",
			want: "timeout",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := Parse([]byte(c.yaml))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			_, err = Expand(s)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if !strings.Contains(strings.ToLower(err.Error()), c.want) {
				t.Errorf("error %q does not mention %q", err.Error(), c.want)
			}
		})
	}
}

// TestExpandAuthExecRejectsUsersAndSource keeps exec mutually exclusive with the
// pool-shaped inputs: exec mints its own token from a command, so inline users / a
// source make no sense and are rejected.
func TestExpandAuthExecRejectsUsersAndSource(t *testing.T) {
	yaml := "target: http://x\nflow:\n  - id: a\n    request: GET /a\nauth:\n  strategy: exec\n  exec:\n    command: [/bin/echo, tok]\n  users:\n    - token: t0\n"
	s, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := Expand(s); err == nil {
		t.Fatal("exec + inline users should be rejected")
	}
}
