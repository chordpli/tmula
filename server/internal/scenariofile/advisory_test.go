package scenariofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestExpandCarriesAuthAdvisories wires the importer's auth advisories (e.g. the
// mint-managed-idp footgun warning, the openIdConnect discovery pointer) through
// Expand onto the spec, so the /import response can surface them to the UI.
func TestExpandCarriesAuthAdvisories(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
		AuthAdvisories: []domain.AuthAdvisory{
			{Code: "mint-managed-idp", Detail: "tenant.auth0.com"},
			{Code: "openidconnect-discovery", Detail: "https://idp/.well-known/openid-configuration"},
		},
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(spec.AuthAdvisories) != 2 ||
		spec.AuthAdvisories[0].Code != "mint-managed-idp" ||
		spec.AuthAdvisories[0].Detail != "tenant.auth0.com" {
		t.Errorf("spec.AuthAdvisories = %+v, want the scenario's two advisories carried through", spec.AuthAdvisories)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded spec failed validation: %v", err)
	}
}

// TestExpandNoAdvisoriesStaysEmpty pins the default: a scenario without
// advisories expands with none (the field is omitempty on the wire).
func TestExpandNoAdvisoriesStaysEmpty(t *testing.T) {
	spec, err := Expand(Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(spec.AuthAdvisories) != 0 {
		t.Errorf("AuthAdvisories = %+v, want empty", spec.AuthAdvisories)
	}
}

// TestAuthSourceMaxBytesOverride: auth.source.maxBytes caps the referenced file
// (the cap itself always stands — the override just moves it), and the cap error
// surfaces through Expand.
func TestAuthSourceMaxBytesOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pool.tokens"), []byte("tok-a\ntok-b\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
		Auth:   &Auth{Strategy: "pool", Source: &AuthSource{File: "pool.tokens", Format: "tokens", MaxBytes: 3}},
	}
	_, err := ExpandFrom(s, dir)
	if err == nil || !strings.Contains(err.Error(), "exceeds the 3-byte limit") {
		t.Fatalf("err = %v, want the 3-byte cap error", err)
	}

	// A generous override loads fine.
	s.Auth.Source.MaxBytes = 1 << 20
	spec, err := ExpandFrom(s, dir)
	if err != nil {
		t.Fatalf("ExpandFrom with a generous cap: %v", err)
	}
	if len(spec.CredentialPool.Entries) != 2 {
		t.Errorf("entries = %d, want 2", len(spec.CredentialPool.Entries))
	}
}

// TestAuthSourceMaxBytesRidesTheRef: ExpandRef ships the maxBytes override on the
// non-secret reference so a worker resolves the file under the same cap.
func TestAuthSourceMaxBytesRidesTheRef(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
		Auth:   &Auth{Strategy: "pool", Source: &AuthSource{File: "pool.tokens", Format: "tokens", MaxBytes: 99}},
	}
	spec, err := ExpandRef(s, "")
	if err != nil {
		t.Fatalf("ExpandRef: %v", err)
	}
	if spec.CredentialPool == nil || spec.CredentialPool.Source == nil {
		t.Fatalf("expected a reference-only pool, got %+v", spec.CredentialPool)
	}
	if spec.CredentialPool.Source.MaxBytes != 99 {
		t.Errorf("ref MaxBytes = %d, want 99", spec.CredentialPool.Source.MaxBytes)
	}
}

// TestUsersPatternMaterializesPool: auth.usersPattern generates N pool entries at
// Expand time (no file), and the SECRET TEMPLATE never appears in the serialized
// RunSpec (entries' secrets are masked; the template is not carried).
func TestUsersPatternMaterializesPool(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
		Auth: &Auth{
			Strategy:     "pool",
			UsersPattern: &AuthUsersPattern{Subject: "user{{.userIndex}}", Token: "SEKRET-{{.userIndex}}", Count: 5},
		},
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || len(spec.CredentialPool.Entries) != 5 {
		t.Fatalf("entries = %+v, want 5", spec.CredentialPool)
	}
	if spec.CredentialPool.Entries[0].Subject != "user0" || spec.CredentialPool.Entries[4].Secret != "SEKRET-4" {
		t.Errorf("entries = %+v, want user0..user4 / SEKRET-0..SEKRET-4", spec.CredentialPool.Entries)
	}
	// The secret TEMPLATE must not survive into the wire spec, and the materialized
	// secrets are masked by Credential.Secret json:"-".
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, needle := range []string{"SEKRET-", "usersPattern", "userIndex"} {
		if strings.Contains(string(b), needle) {
			t.Errorf("serialized RunSpec leaked %q: %s", needle, b)
		}
	}
}

// TestUsersPatternFeedsLogin: usersPattern also feeds the login strategy as
// login-INPUT rows (subject=username, token=password).
func TestUsersPatternFeedsLogin(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
		Auth: &Auth{
			Strategy:     "login",
			UsersPattern: &AuthUsersPattern{Subject: "user{{.userIndex}}", Token: "pw-{{.userIndex}}", Count: 3},
			Login: &AuthLogin{
				Flow: []Step{{ID: "login", Request: "POST /login", Body: `{"u":"{{.username}}"}`}},
			},
		},
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || len(spec.CredentialPool.Entries) != 3 {
		t.Fatalf("login entries = %+v, want 3 rows", spec.CredentialPool)
	}
	if spec.CredentialPool.Entries[1].Subject != "user1" || spec.CredentialPool.Entries[1].Secret != "pw-1" {
		t.Errorf("row 1 = %+v, want user1/pw-1", spec.CredentialPool.Entries[1])
	}
}

// TestUsersPatternMutualExclusion: usersPattern conflicts with inline users and
// with a source.
func TestUsersPatternMutualExclusion(t *testing.T) {
	base := func() Scenario {
		return Scenario{
			Target: "http://h:1",
			Flow:   []Step{{ID: "a", Request: "GET /a"}},
		}
	}
	withUsers := base()
	withUsers.Auth = &Auth{Strategy: "pool", Users: []Credential{{Subject: "a", Token: "t"}}, UsersPattern: &AuthUsersPattern{Token: "t{{.userIndex}}", Count: 1}}
	if _, err := Expand(withUsers); err == nil {
		t.Error("usersPattern + inline users should be rejected")
	}
	withSource := base()
	withSource.Auth = &Auth{Strategy: "pool", Source: &AuthSource{File: "u.csv", Format: "csv"}, UsersPattern: &AuthUsersPattern{Token: "t{{.userIndex}}", Count: 1}}
	if _, err := Expand(withSource); err == nil {
		t.Error("usersPattern + source should be rejected")
	}
}
