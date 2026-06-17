package scenariofile

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

const bootstrapAuthYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  strategy: bootstrap-signup
  signup:
    flow:
      - id: register
        request: POST /signup
        body: '{"i":"{{.userIndex}}"}'
        extract:
          token: accessToken
          uid: id
    teardown:
      - id: remove
        request: DELETE /accounts/{{.subject}}
    capture:
      token: token
      subject: uid
`

// TestExpandAuthBootstrap threads a compact bootstrap-signup auth block into the
// RunSpec: the strategy is CredBootstrapSignup, the pool carries the declarative
// SignupFlow (signup + teardown steps + captures), and the spec validates (it has a
// teardown, so the gating-safety gate is satisfied).
func TestExpandAuthBootstrap(t *testing.T) {
	s, err := Parse([]byte(bootstrapAuthYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || spec.CredentialPool.Strategy != domain.CredBootstrapSignup {
		t.Fatalf("expected a bootstrap-signup pool, got %+v", spec.CredentialPool)
	}
	sf := spec.CredentialPool.SignupFlow
	if sf == nil {
		t.Fatal("expanded bootstrap spec carries no signup flow")
	}
	if sf.Capture.Token != "token" || sf.Capture.Subject != "uid" {
		t.Errorf("captures = %+v, want token/uid", sf.Capture)
	}
	if len(sf.Steps) != 1 || sf.Steps[0].Method != "POST" || sf.Steps[0].Path != "/signup" {
		t.Errorf("signup steps = %+v, want one POST /signup", sf.Steps)
	}
	if !sf.HasTeardown() {
		t.Error("bootstrap flow should carry a teardown journey")
	}
	if len(sf.Teardown) != 1 || sf.Teardown[0].Method != "DELETE" {
		t.Errorf("teardown steps = %+v, want one DELETE", sf.Teardown)
	}
	if spec.Experiment.Params.AuthStrategy != domain.CredBootstrapSignup {
		t.Errorf("experiment auth strategy = %q, want bootstrap-signup", spec.Experiment.Params.AuthStrategy)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded bootstrap spec failed validation: %v", err)
	}
}

// TestExpandAuthBootstrapWithoutTeardownRejected pins the gating-safety rule at the
// scenariofile layer: a bootstrap block with a signup flow but no teardown and no
// keepAccounts expands but fails validation (so `tmula run` refuses it).
func TestExpandAuthBootstrapWithoutTeardownRejected(t *testing.T) {
	noTeardown := strings.Replace(bootstrapAuthYAML, `    teardown:
      - id: remove
        request: DELETE /accounts/{{.subject}}
`, "", 1)
	s, err := Parse([]byte(noTeardown))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if err := spec.Validate(); err == nil {
		t.Fatal("a bootstrap scenario with no teardown and no keepAccounts must fail validation")
	}
}

// TestExpandAuthBootstrapKeepAccounts proves the keepAccounts opt-out lets a
// no-teardown bootstrap scenario validate.
func TestExpandAuthBootstrapKeepAccounts(t *testing.T) {
	keep := strings.Replace(bootstrapAuthYAML, `    teardown:
      - id: remove
        request: DELETE /accounts/{{.subject}}
`, "", 1)
	keep = strings.Replace(keep, "  strategy: bootstrap-signup\n", "  strategy: bootstrap-signup\n  keepAccounts: true\n", 1)
	s, err := Parse([]byte(keep))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !spec.CredentialPool.KeepAccounts {
		t.Error("keepAccounts not threaded onto the pool")
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("keepAccounts bootstrap scenario should validate, got: %v", err)
	}
}
