package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// wellFormedSignupFlow is a minimal valid signup flow: one POST that captures a
// token from the response, plus a teardown DELETE keyed by the captured subject.
func wellFormedSignupFlow() *SignupFlow {
	return &SignupFlow{
		Steps: []SignupStep{{
			ID: "register", Method: "POST", Path: "/signup",
			Body:    `{"i":"{{.userIndex}}"}`,
			Extract: map[string]string{"token": "accessToken", "uid": "id"},
		}},
		Start:   "register",
		Capture: SignupCapture{Token: "token", Subject: "uid"},
		Teardown: []SignupStep{{
			ID: "remove", Method: "DELETE", Path: "/accounts/{{.subject}}",
		}},
		TeardownStart: "remove",
	}
}

// TestSignupFlowValidate pins the declarative signup-flow shape the bootstrap
// strategy compiles: a non-empty step list, a start node present in it, a
// resolvable token (secret) capture, and well-formed steps.
func TestSignupFlowValidate(t *testing.T) {
	if err := wellFormedSignupFlow().Validate(); err != nil {
		t.Fatalf("well-formed signup flow rejected: %v", err)
	}

	// No steps: rejected.
	empty := &SignupFlow{Capture: SignupCapture{Token: "token"}, Start: "x"}
	if err := empty.Validate(); err == nil {
		t.Error("signup flow with no steps should be rejected")
	}

	// Start node not in the flow: rejected.
	badStart := wellFormedSignupFlow()
	badStart.Start = "nope"
	if err := badStart.Validate(); err == nil {
		t.Error("signup flow whose start node is not a step should be rejected")
	}

	// No token capture: ACCEPTED — an empty token capture means the runner
	// auto-detects the token from the signup response, so the flow is still valid.
	noToken := wellFormedSignupFlow()
	noToken.Capture.Token = ""
	if err := noToken.Validate(); err != nil {
		t.Errorf("signup flow with no explicit token capture (auto-detect) should be valid: %v", err)
	}

	// A step with no method/path is malformed.
	badStep := wellFormedSignupFlow()
	badStep.Steps[0].Method = ""
	if err := badStep.Validate(); err == nil {
		t.Error("signup flow with a method-less step should be rejected")
	}

	// Duplicate step ids are rejected.
	dup := wellFormedSignupFlow()
	dup.Steps = append(dup.Steps, SignupStep{ID: "register", Method: "GET", Path: "/x"})
	if err := dup.Validate(); err == nil {
		t.Error("signup flow with duplicate step ids should be rejected")
	}

	// A teardown start that is not a teardown step is rejected.
	badTeardown := wellFormedSignupFlow()
	badTeardown.TeardownStart = "ghost"
	if err := badTeardown.Validate(); err == nil {
		t.Error("signup flow whose teardown start is not a teardown step should be rejected")
	}
}

// TestSignupFlowHasTeardown reports whether the flow declares a teardown journey,
// the gating-safety signal the rejection-lift path keys on.
func TestSignupFlowHasTeardown(t *testing.T) {
	if !wellFormedSignupFlow().HasTeardown() {
		t.Error("a flow with teardown steps should report HasTeardown")
	}
	noTeardown := wellFormedSignupFlow()
	noTeardown.Teardown = nil
	noTeardown.TeardownStart = ""
	if noTeardown.HasTeardown() {
		t.Error("a flow with no teardown steps should not report HasTeardown")
	}
}

// TestCredentialPoolValidateBootstrapSignupFlow pins the third edit of the
// CredentialPool Validate: a bootstrap-signup pool is valid when it carries a
// well-formed SignupFlow (the declarative form the orchestrator compiles), in
// addition to the legacy BootstrapFlowID form, and a malformed SignupFlow is
// rejected. The Source/login branches are untouched.
func TestCredentialPoolValidateBootstrapSignupFlow(t *testing.T) {
	// Declarative SignupFlow form: valid.
	pool := CredentialPool{ID: "p", Strategy: CredBootstrapSignup, SignupFlow: wellFormedSignupFlow()}
	if err := pool.Validate(); err != nil {
		t.Errorf("bootstrap pool with a well-formed signup flow rejected: %v", err)
	}

	// A SignupFlow with no explicit token capture is ACCEPTED: the empty token
	// capture means the runner auto-detects the token from the signup response.
	noSecret := wellFormedSignupFlow()
	noSecret.Capture.Token = ""
	autoDetect := CredentialPool{ID: "p", Strategy: CredBootstrapSignup, SignupFlow: noSecret}
	if err := autoDetect.Validate(); err != nil {
		t.Errorf("bootstrap pool whose signup flow auto-detects the token should be valid: %v", err)
	}

	// The legacy BootstrapFlowID form still validates (P1/P2 invariant).
	flow := ID("signup")
	legacy := CredentialPool{ID: "p", Strategy: CredBootstrapSignup, BootstrapFlowID: &flow}
	if err := legacy.Validate(); err != nil {
		t.Errorf("legacy bootstrap pool (flow id) rejected: %v", err)
	}

	// Neither a flow id nor a signup flow: still rejected.
	neither := CredentialPool{ID: "p", Strategy: CredBootstrapSignup}
	if err := neither.Validate(); err == nil {
		t.Error("bootstrap pool with neither a flow id nor a signup flow should be rejected")
	}
}

// TestCredentialPoolBootstrapMarshalNoSecret confirms a bootstrap pool with a
// signup flow and the keep-accounts intent serializes its declarative fields but
// never a secret — the account tokens are minted at run time and never authored.
func TestCredentialPoolBootstrapMarshalNoSecret(t *testing.T) {
	pool := CredentialPool{ID: "p", Strategy: CredBootstrapSignup, SignupFlow: wellFormedSignupFlow(), KeepAccounts: true}
	b, err := json.Marshal(pool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, "signupFlow") {
		t.Errorf("bootstrap pool dropped its declarative signup flow: %s", out)
	}
	if !strings.Contains(out, "keepAccounts") {
		t.Errorf("bootstrap pool dropped the keep-accounts intent: %s", out)
	}
	// The secret field on a domain.Credential is json:"-"; the signup flow carries
	// no secret of its own, so nothing secret-shaped should appear.
	if strings.Contains(out, "\"secret\"") {
		t.Errorf("bootstrap pool leaked a secret-shaped field: %s", out)
	}
}

// TestCredentialPoolKeepAccountsOmitemptyByteCompat proves the new KeepAccounts and
// SignupFlow fields are omitempty: a pool that sets neither marshals exactly as it
// did before the fields existed (no byte drift for existing pools).
func TestCredentialPoolKeepAccountsOmitemptyByteCompat(t *testing.T) {
	pool := CredentialPool{ID: "p", Strategy: CredPool, Entries: []Credential{{Subject: "u0"}}}
	b, err := json.Marshal(pool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, f := range []string{"signupFlow", "keepAccounts"} {
		if strings.Contains(out, f) {
			t.Errorf("a non-bootstrap pool serialized the bootstrap field %q: %s", f, out)
		}
	}
}
