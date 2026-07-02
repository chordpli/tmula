package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// TestUsersPatternPoolRunAtScale closes the Phase 2 gate: a scenario declaring
// auth.usersPattern with a large count expands into a materialized pool (no file)
// and runs end to end, every virtual user sending its own patterned token. It
// proves the scenariofile -> Expand -> run chain at scale without a credential
// file, and that the secret pattern never has to cross a wire (the pool is
// resolved in-process).
func TestUsersPatternPoolRunAtScale(t *testing.T) {
	const users = 10000
	tokenRE := regexp.MustCompile(`^Bearer tok-\d+$`)
	var mu sync.Mutex
	unauthorized := 0
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenRE.MatchString(r.Header.Get("Authorization")) {
			mu.Lock()
			unauthorized++
			mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	// A compact scenario file: one GET carrying the patterned token, a pool of
	// `users` accounts generated from a pattern (no file).
	sc := scenariofile.Scenario{
		Target: sut.URL,
		Allow:  []string{"127.0.0.1"},
		Users:  users,
		Flow: []scenariofile.Step{
			{ID: "hit", Request: "GET /data", Headers: map[string]string{"Authorization": "Bearer {{.token}}"}},
		},
		Auth: &scenariofile.Auth{
			Strategy:     "pool",
			UsersPattern: &scenariofile.AuthUsersPattern{Token: "tok-{{.userIndex}}", Count: users},
		},
	}
	spec, err := scenariofile.Expand(sc)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.CredentialPool == nil || len(spec.CredentialPool.Entries) != users {
		t.Fatalf("pool entries = %d, want %d (pattern must materialize the whole pool)", len(spec.CredentialPool.Entries), users)
	}
	// Bound in-flight so the httptest SUT is not overwhelmed by the default 1000
	// concurrent connections (which yields transport EOFs unrelated to auth); the
	// run still sends one request per patterned user, proving the path at scale.
	spec.TargetEnv.RateCap.MaxConcurrency = 50

	rep := runInProcess(t, spec, 30*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q (reason %q), want completed", rep.Run.Status, rep.Run.KillReason)
	}
	if rep.Stats.Total != users {
		t.Errorf("stats.Total = %d, want %d (one request per patterned user)", rep.Stats.Total, users)
	}
	if rep.Stats.Errors != 0 {
		t.Errorf("stats.Errors = %d, want 0", rep.Stats.Errors)
	}
	mu.Lock()
	defer mu.Unlock()
	if unauthorized != 0 {
		t.Errorf("SUT saw %d unauthorized requests, want 0 — a patterned token did not reach a request", unauthorized)
	}

	// Belt-and-suspenders: the materialized secret pattern must not survive into a
	// serialized RunSpec (entries' secrets are json:"-", the template is not carried).
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if strings.Contains(string(b), "tok-{{.userIndex}}") || strings.Contains(string(b), "usersPattern") {
		t.Error("the secret pattern template leaked into the serialized RunSpec")
	}
}
