package importer

import (
	"encoding/json"
	"strings"

	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// signupKeywords mark an operation (by operationId, path or tag) as a
// registration / account-creation endpoint. They are a superset of the login
// classifier's register words, kept local so the signup suggestion is independent
// of the login-ordering keywords.
var signupKeywords = []string{"register", "signup", "sign-up", "sign_up"}

// deriveSignup builds an advisory signup suggestion from a register/signup
// operation, INDEPENDENT of the primary auth (a securityScheme may have derived a
// login while a signup is still offered separately). It is best-effort: no
// register operation yields a nil suggestion (no signup offered), so a spec
// without one imports exactly as before.
//
// The suggestion is a single signup step (POST the register path) with the body
// templated from the operation's requestBody example: the identity field
// (email/username) is rewritten to a per-VU unique value the signup runner renders
// per virtual user, and any password-like field is marked REPLACE_ME_PASSWORD so a
// secret is never carried from the spec. The token capture is left empty — E1
// auto-detects the token in the signup response. When a DELETE on a user resource
// exists, a teardown step deleting the provisioned account by {{.subject}} is
// derived; otherwise teardown is empty (the run then needs --keep-accounts, which
// the UI surfaces).
func deriveSignup(ops []apiOp) *scenariofile.AuthSignup {
	op, ok := findSignupOp(ops)
	if !ok {
		return nil
	}
	if !safeRequestPath(op.path) {
		return nil // a malformed register path cannot yield a runnable step
	}

	signup := &scenariofile.AuthSignup{
		Flow: []scenariofile.Step{{
			ID:      "signup",
			Request: strings.ToUpper(op.method) + " " + op.path,
			Body:    signupBodyFrom(op.op),
		}},
		// Capture.Token intentionally empty — E1 auto-detects the token in the response.
	}
	if td, ok := deriveTeardownStep(ops); ok {
		signup.Teardown = []scenariofile.Step{td}
	}
	return signup
}

// findSignupOp returns the first POST operation whose operationId, path or a tag
// names a registration/account-creation endpoint. Only a POST qualifies — a GET
// /register (a form page) or a DELETE is not an account-minting call.
func findSignupOp(ops []apiOp) (apiOp, bool) {
	for _, o := range ops {
		if o.method != "post" {
			continue
		}
		if looksLikeSignup(o) {
			return o, true
		}
	}
	return apiOp{}, false
}

// looksLikeSignup reports whether an operation reads as a registration endpoint:
// a register/signup keyword in its operationId, path or tags, or the "users
// create" shape (a POST to a /users collection).
func looksLikeSignup(o apiOp) bool {
	hay := strings.ToLower(o.op.OperationID + " " + o.path + " " + strings.Join(o.op.Tags, " "))
	if matchesAny(hay, signupKeywords...) {
		return true
	}
	// "users create": a POST to a /users (or /accounts) collection endpoint — not a
	// nested or parameterized path (those address an existing resource).
	return isUserCollectionPath(o.path)
}

// isUserCollectionPath reports whether a path is a top-level user/account
// collection (e.g. "/users", "/accounts"), so a POST to it is an account-create.
// A parameterized or deeper path (/users/{id}, /users/{id}/posts) addresses an
// existing resource and does not qualify.
func isUserCollectionPath(path string) bool {
	trimmed := strings.Trim(strings.ToLower(path), "/")
	switch trimmed {
	case "users", "accounts":
		return true
	default:
		return false
	}
}

// signupBodyFrom builds the signup request body from an operation's requestBody
// example: the identity field (email/username/login) is rewritten to a per-VU
// unique template value so each virtual user provisions a distinct account, and
// any password-like field is marked REPLACE_ME_PASSWORD. With no example it falls
// back to a minimal email+password object. The body never carries an example
// secret or the example's literal identity.
func signupBodyFrom(op operation) string {
	ex := bodyExample(op)
	if ex == "" {
		return defaultSignupBody()
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ex), &obj); err != nil {
		// Not a JSON object we can rewrite safely; use the minimal body rather than
		// leaking an arbitrary example that may carry a literal secret.
		return defaultSignupBody()
	}
	templatedIdentity := false
	for k := range obj {
		switch {
		case isPasswordField(k):
			obj[k] = json.RawMessage(`"REPLACE_ME_PASSWORD"`)
		case isIdentityField(k):
			obj[k] = json.RawMessage(uniqueIdentityFor(k))
			templatedIdentity = true
		}
	}
	// If the example named no recognizable identity field, the body would carry no
	// per-VU uniqueness — fall back to the minimal body that guarantees it.
	if !templatedIdentity {
		return defaultSignupBody()
	}
	if b, err := json.Marshal(obj); err == nil {
		return string(b)
	}
	return defaultSignupBody()
}

// defaultSignupBody is the minimal signup payload used when no usable example
// exists: a per-VU unique email and the password placeholder.
func defaultSignupBody() string {
	return `{"email":"` + uniqueEmailIdentity + `","password":"REPLACE_ME_PASSWORD"}`
}

// uniqueEmailIdentity is the per-VU unique email template: {{.userIndex}} is the
// per-virtual-user variable the signup runner threads into the render context (see
// load.NewSignupRunner), so each virtual user provisions a distinct account.
const uniqueEmailIdentity = "tester+{{.userIndex}}@example.test"

// uniqueUsernameIdentity is the per-VU unique username (used when the identity
// field is a username rather than an email).
const uniqueUsernameIdentity = "tester{{.userIndex}}"

// uniqueIdentityFor returns the JSON-encoded per-VU unique value for an identity
// field: an email-shaped one for an email field, else a plain unique username.
func uniqueIdentityFor(field string) string {
	if strings.Contains(strings.ToLower(field), "email") {
		return `"` + uniqueEmailIdentity + `"`
	}
	return `"` + uniqueUsernameIdentity + `"`
}

// isIdentityField reports whether a requestBody field names the account's identity
// (the field that must be unique per virtual user): email/username/login/handle.
func isIdentityField(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "email") || strings.Contains(n, "username") ||
		n == "user" || n == "login" || strings.Contains(n, "handle")
}

// deriveTeardownStep derives a teardown step from a DELETE operation on a user
// resource (e.g. "DELETE /users/{id}"): it rewrites the single path parameter to
// {{.subject}} so the teardown deletes the exact account the signup provisioned
// (whose subject E1 auto-detects). It returns ok=false when no such operation
// exists, so the suggestion is left teardown-less.
func deriveTeardownStep(ops []apiOp) (scenariofile.Step, bool) {
	for _, o := range ops {
		if o.method != "delete" {
			continue
		}
		if !isUserResourcePath(o.path) {
			continue
		}
		path := subjectTemplatedPath(o.path)
		if !safeRequestPath(path) {
			continue
		}
		return scenariofile.Step{
			ID:      "teardown_signup",
			Request: "DELETE " + path,
		}, true
	}
	return scenariofile.Step{}, false
}

// isUserResourcePath reports whether a path addresses a single user/account
// resource by id: a /users/{id} (or /accounts/{id}) shape with exactly one path
// parameter under the user collection.
func isUserResourcePath(path string) bool {
	segs := splitPathSegments(path)
	if len(segs) != 2 {
		return false
	}
	head := strings.ToLower(segs[0])
	if head != "users" && head != "accounts" {
		return false
	}
	return isPathParam(segs[1])
}

// subjectTemplatedPath rewrites the single path parameter of a user-resource path
// to {{.subject}}, so the teardown deletes the provisioned account by its subject.
func subjectTemplatedPath(path string) string {
	segs := splitPathSegments(path)
	for i, s := range segs {
		if isPathParam(s) {
			segs[i] = "{{.subject}}"
		}
	}
	return "/" + strings.Join(segs, "/")
}

// splitPathSegments splits a rooted path into its non-empty segments.
func splitPathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// isPathParam reports whether a path segment is an OpenAPI path parameter
// ("{id}", "{userId}", …).
func isPathParam(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && len(seg) > 2
}
