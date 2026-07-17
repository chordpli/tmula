package importer

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// TestSignupDerivedFromRegisterOp derives a signup suggestion from an OpenAPI
// `/register` POST: a single signup step posting that path, the body templated
// from the operation's example with a per-VU unique identity ({{.userIndex}}) and
// the password marked REPLACE_ME_PASSWORD, the token capture left empty (E1
// auto-detects), and — because a DELETE on a user resource exists — a teardown
// step that deletes the provisioned account by {{.subject}}.
func TestSignupDerivedFromRegisterOp(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  /register:
    post:
      operationId: registerUser
      requestBody:
        content:
          application/json:
            example: { "email": "alice@example.com", "password": "s3cret" }
  /users/{id}:
    delete:
      operationId: deleteUser
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	su := s.SuggestedSignup
	if su == nil {
		t.Fatal("expected a suggested signup, got nil")
	}
	if len(su.Flow) != 1 {
		t.Fatalf("signup flow = %+v, want a single step", su.Flow)
	}
	step := su.Flow[0]
	if step.Request != "POST /register" {
		t.Errorf("signup request = %q, want POST /register", step.Request)
	}
	// Per-VU unique identity: the email field is templated with {{.userIndex}} so
	// every virtual user provisions a distinct account.
	if !strings.Contains(step.Body, "{{.userIndex}}") {
		t.Errorf("signup body = %q, want a per-VU unique identity templated with {{.userIndex}}", step.Body)
	}
	// The example identity must not leak verbatim; it is rewritten to a unique one.
	if strings.Contains(step.Body, "alice@example.com") {
		t.Errorf("signup body = %q, must not carry the example identity verbatim", step.Body)
	}
	if !strings.Contains(step.Body, "REPLACE_ME_PASSWORD") {
		t.Errorf("signup body = %q, want the password field marked REPLACE_ME_PASSWORD", step.Body)
	}
	if strings.Contains(step.Body, "s3cret") {
		t.Errorf("signup body = %q, must not leak the example password", step.Body)
	}
	// The body must render as a Go text/template (the signup runner renders it).
	assertTemplateSafe(t, "signup body", step.Body)
	if su.Capture.Token != "" {
		t.Errorf("signup token capture = %q, want empty (E1 auto-detects)", su.Capture.Token)
	}
	// A DELETE on a user resource exists, so a teardown step is derived.
	if len(su.Teardown) != 1 {
		t.Fatalf("teardown = %+v, want a single delete step (a DELETE /users/{id} op exists)", su.Teardown)
	}
	td := su.Teardown[0]
	if !strings.HasPrefix(td.Request, "DELETE ") {
		t.Errorf("teardown request = %q, want a DELETE", td.Request)
	}
	if !strings.Contains(td.Request, "{{.subject}}") {
		t.Errorf("teardown request = %q, want the user id templated with {{.subject}}", td.Request)
	}
	assertTemplateSafe(t, "teardown request", td.Request)
}

// TestSignupDerivedNoTeardownWhenNoDelete derives the signup step but leaves the
// teardown empty when no user-resource DELETE operation exists (the run then
// requires --keep-accounts, which the UI surfaces).
func TestSignupDerivedNoTeardownWhenNoDelete(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  /signup:
    post:
      operationId: signUp
      requestBody:
        content:
          application/json:
            example: { "username": "bob", "password": "hunter2" }
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	su := s.SuggestedSignup
	if su == nil {
		t.Fatal("expected a suggested signup, got nil")
	}
	if len(su.Flow) != 1 || su.Flow[0].Request != "POST /signup" {
		t.Fatalf("signup flow = %+v, want a single POST /signup step", su.Flow)
	}
	if !strings.Contains(su.Flow[0].Body, "{{.userIndex}}") {
		t.Errorf("signup body = %q, want a per-VU unique identity", su.Flow[0].Body)
	}
	if !strings.Contains(su.Flow[0].Body, "REPLACE_ME_PASSWORD") {
		t.Errorf("signup body = %q, want REPLACE_ME_PASSWORD", su.Flow[0].Body)
	}
	if len(su.Teardown) != 0 {
		t.Errorf("teardown = %+v, want empty (no DELETE on a user resource)", su.Teardown)
	}
}

// TestSignupNotDerivedWhenNoRegisterOp emits no signup suggestion for a spec
// with no register/signup operation (backward-compat control).
func TestSignupNotDerivedWhenNoRegisterOp(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  /items:
    get:
      operationId: listItems
  /items/{id}:
    delete:
      operationId: deleteItem
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.SuggestedSignup != nil {
		t.Errorf("suggested signup = %+v, want nil (no register/signup op)", s.SuggestedSignup)
	}
}

// TestSignupDerivedSchemaLessRegisterOp derives a minimal email+password signup
// body when the register op carries no requestBody example (a per-VU unique
// identity and the password placeholder, still no leaked secret).
func TestSignupDerivedSchemaLessRegisterOp(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  /register:
    post:
      operationId: register
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	su := s.SuggestedSignup
	if su == nil || len(su.Flow) != 1 {
		t.Fatalf("suggested signup = %+v, want a single signup step", su)
	}
	body := su.Flow[0].Body
	if !strings.Contains(body, "{{.userIndex}}") {
		t.Errorf("signup body = %q, want a per-VU unique identity", body)
	}
	if !strings.Contains(body, "REPLACE_ME_PASSWORD") {
		t.Errorf("signup body = %q, want REPLACE_ME_PASSWORD", body)
	}
	assertTemplateSafe(t, "schema-less signup body", body)
}

// TestSignupSuggestionExpandsToDomainFlow confirms the suggested signup threads
// through scenariofile.Expand onto the RunSpec as a domain.SignupFlow, the wire
// shape the import endpoint returns.
func TestSignupSuggestionExpandsToDomainFlow(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  /register:
    post:
      operationId: register
      requestBody:
        content:
          application/json:
            example: { "email": "alice@example.com", "password": "s3cret" }
  /users/{id}:
    delete:
      operationId: deleteUser
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	spec, err := scenariofile.Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.SuggestedSignup == nil {
		t.Fatal("expanded spec carries no suggested signup")
	}
	if len(spec.SuggestedSignup.Steps) != 1 {
		t.Fatalf("suggested signup steps = %+v, want one", spec.SuggestedSignup.Steps)
	}
	st := spec.SuggestedSignup.Steps[0]
	if st.Method != "POST" || st.Path != "/register" {
		t.Errorf("signup step = %s %s, want POST /register", st.Method, st.Path)
	}
	if !strings.Contains(st.Body, "{{.userIndex}}") {
		t.Errorf("signup step body = %q, want a per-VU unique identity", st.Body)
	}
	if !spec.SuggestedSignup.HasTeardown() {
		t.Error("suggested signup should carry a teardown step (a DELETE /users/{id} op exists)")
	}
	// The suggested flow is well-formed.
	if err := spec.SuggestedSignup.Validate(); err != nil {
		t.Errorf("suggested signup flow failed validation: %v", err)
	}
}
