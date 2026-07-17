package scenariofile

import (
	"strings"
	"testing"
)

// baseFlow is a minimal main journey so Expand reaches the auth block.
func baseFlow() []Step {
	return []Step{{ID: "a", Request: "GET /a"}}
}

// TestExpandRejectsReplaceMePlaceholders proves the expand-time guard refuses an
// importer-scaffolded auth block that still carries a REPLACE_ME_* placeholder, for
// every strategy that can hold one, and that the error names the exact location and
// token so an operator knows precisely what to fill.
func TestExpandRejectsReplaceMePlaceholders(t *testing.T) {
	cases := []struct {
		name    string
		auth    Auth
		wantLoc string // substring naming the location
		wantTok string // the exact placeholder token
	}{
		{
			name:    "inline pool token",
			auth:    Auth{Strategy: "pool", Users: []Credential{{Subject: "alice", Token: "REPLACE_ME_TOKEN"}}},
			wantLoc: `auth.users[0].token`,
			wantTok: "REPLACE_ME_TOKEN",
		},
		{
			name:    "usersPattern token",
			auth:    Auth{Strategy: "pool", UsersPattern: &AuthUsersPattern{Token: "REPLACE_ME_PW", Count: 3}},
			wantLoc: `auth.usersPattern.token`,
			wantTok: "REPLACE_ME_PW",
		},
		{
			name:    "usersPattern subject",
			auth:    Auth{Strategy: "pool", UsersPattern: &AuthUsersPattern{Subject: "REPLACE_ME_USER", Token: "pw", Count: 3}},
			wantLoc: `auth.usersPattern.subject`,
			wantTok: "REPLACE_ME_USER",
		},
		{
			name: "login flow body",
			auth: Auth{
				Strategy: "login",
				Login: &AuthLogin{Flow: []Step{{
					ID:      "login",
					Request: "POST /login",
					Body:    `{"username":"alice","password":"REPLACE_ME_PASSWORD"}`,
				}}},
			},
			wantLoc: `auth.login.flow step "login"`,
			wantTok: "REPLACE_ME_PASSWORD",
		},
		{
			name: "login flow header",
			auth: Auth{
				Strategy: "login",
				Login: &AuthLogin{Flow: []Step{{
					ID:      "tok",
					Request: "POST /token",
					Headers: map[string]string{"Authorization": "Basic REPLACE_ME_BASICAUTH"},
				}}},
			},
			wantLoc: `auth.login.flow step "tok"`,
			wantTok: "REPLACE_ME_BASICAUTH",
		},
		{
			name: "signup flow body",
			auth: Auth{
				Strategy: "bootstrap-signup",
				Signup: &AuthSignup{
					Flow:     []Step{{ID: "signup", Request: "POST /signup", Body: `{"pw":"REPLACE_ME_SECRET"}`}},
					Teardown: []Step{{ID: "rm", Request: "DELETE /u/{{.subject}}"}},
				},
			},
			wantLoc: `auth.signup.flow step "signup"`,
			wantTok: "REPLACE_ME_SECRET",
		},
		{
			name: "signup teardown header",
			auth: Auth{
				Strategy: "bootstrap-signup",
				Signup: &AuthSignup{
					Flow: []Step{{ID: "signup", Request: "POST /signup", Body: `{"u":"u{{.userIndex}}","pw":"x"}`}},
					Teardown: []Step{{
						ID:      "rm",
						Request: "DELETE /u/{{.subject}}",
						Headers: map[string]string{"Authorization": "Bearer REPLACE_ME_ADMIN_TOKEN"},
					}},
				},
			},
			wantLoc: `auth.signup.teardown step "rm"`,
			wantTok: "REPLACE_ME_ADMIN_TOKEN",
		},
		{
			name: "exec command argv",
			auth: Auth{
				Strategy: "exec",
				Exec:     &AuthExec{Command: []string{"/bin/token", "--key", "REPLACE_ME_APIKEY"}},
			},
			wantLoc: `auth.exec.command`,
			wantTok: "REPLACE_ME_APIKEY",
		},
		{
			name: "exec env value",
			auth: Auth{
				Strategy: "exec",
				Exec:     &AuthExec{Command: []string{"/bin/token"}, Env: map[string]string{"API_KEY": "REPLACE_ME_ENVKEY"}},
			},
			wantLoc: `auth.exec.env`,
			wantTok: "REPLACE_ME_ENVKEY",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Scenario{Target: "http://h:1", Flow: baseFlow(), Auth: &tc.auth}
			_, err := Expand(s)
			if err == nil {
				t.Fatalf("Expand should reject an unfilled %s placeholder", tc.wantTok)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantLoc) {
				t.Errorf("error should name location %q, got %q", tc.wantLoc, msg)
			}
			if !strings.Contains(msg, tc.wantTok) {
				t.Errorf("error should name the exact placeholder %q, got %q", tc.wantTok, msg)
			}
			if !strings.Contains(msg, "--auth-source") {
				t.Errorf("error should offer the --auth-source escape, got %q", msg)
			}
		})
	}
}

// TestExpandAcceptsFilledSecrets confirms the guard does NOT fire once the placeholder
// is replaced with a real secret — a filled-in pool expands cleanly.
func TestExpandAcceptsFilledSecrets(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   baseFlow(),
		Auth:   &Auth{Strategy: "pool", Users: []Credential{{Subject: "alice", Token: "eyJhbGciOi.real.token"}}},
	}
	if _, err := Expand(s); err != nil {
		t.Fatalf("a filled-in pool must expand: %v", err)
	}
}

// TestExpandReplaceMeGuardIgnoresKeyReferences proves the guard does not flag a
// non-secret key/source REFERENCE — those name where a secret lives, not the secret
// itself, so a REPLACE_ME there is out of scope (and mint uses only references).
func TestExpandReplaceMeGuardIgnoresKeyReferences(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   baseFlow(),
		Auth: &Auth{
			Strategy: "mint",
			Mint: &AuthMint{
				Alg:            "HS256",
				SecretEncoding: "raw",
				Key:            &AuthMintKey{Env: "TMULA_SIGNING_KEY"},
				Subject:        "user-{{.userIndex}}",
				Ttl:            "1h",
			},
		},
	}
	if _, err := Expand(s); err != nil {
		t.Fatalf("a mint block referencing a key by env should expand: %v", err)
	}
}
