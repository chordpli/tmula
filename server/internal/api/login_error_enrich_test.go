package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// newRejectingLoginSUT is a system under test whose /login endpoint always 401s (bad
// credentials), so a login run fails during prewarm and surfaces the enriched error.
func newRejectingLoginSUT() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return httptest.NewServer(mux)
}

// TestPrewarmLoginErrorNamesSubjectAndTarget proves a failed login prewarm reports the
// row SUBJECT that could not authenticate and the request target (POST /login), not just
// a bare "runOnce returned 401", and never echoes the password.
func TestPrewarmLoginErrorNamesSubjectAndTarget(t *testing.T) {
	sut := newRejectingLoginSUT()
	defer sut.Close()

	spec := specLogin(sut.URL, 1, "")
	// A login-INPUT row: subject is the username (non-secret), token is the password.
	spec.CredentialPool.Entries = []domain.Credential{{Subject: "alice", Secret: "hunter2"}}

	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunFailed {
		t.Fatalf("status = %q, want failed (a login that 401s must fail the run)", rep.Run.Status)
	}
	msg := rep.Run.KillReason
	for _, needle := range []string{`subject "alice"`, "POST /login", "401", "user 0"} {
		if !strings.Contains(msg, needle) {
			t.Errorf("enriched login error should mention %q, got %q", needle, msg)
		}
	}
	if strings.Contains(msg, "hunter2") {
		t.Errorf("enriched login error must never echo the password, got %q", msg)
	}
}

// TestPrewarmLoginErrorFlagsReplaceMe proves that when a programmatically-built login
// flow still carries a REPLACE_ME_* placeholder (bypassing the scenariofile expand
// guard), the prewarm failure appends the fill-it-in hint — without echoing the body.
func TestPrewarmLoginErrorFlagsReplaceMe(t *testing.T) {
	sut := newRejectingLoginSUT()
	defer sut.Close()

	spec := specLogin(sut.URL, 1, "")
	// A login body still holding the importer placeholder — the token endpoint 401s.
	spec.LoginFlow.Templates["tlogin"] = domain.APITemplate{
		Method:          "POST",
		Path:            "/login",
		Headers:         map[string]string{"Content-Type": "application/json"},
		PayloadTemplate: `{"username":"alice","password":"REPLACE_ME_PASSWORD"}`,
		Extract:         map[string]string{"token": "access_token"},
	}

	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunFailed {
		t.Fatalf("status = %q, want failed", rep.Run.Status)
	}
	msg := rep.Run.KillReason
	if !strings.Contains(msg, "REPLACE_ME_PASSWORD") && !strings.Contains(msg, "REPLACE_ME_* placeholder") {
		t.Errorf("error should flag the unfilled placeholder, got %q", msg)
	}
	if !strings.Contains(msg, "--auth-source") {
		t.Errorf("error should offer the --auth-source escape, got %q", msg)
	}
}
