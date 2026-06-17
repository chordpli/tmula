package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// bootstrapRunSUT serves a signup that mints a unique account, a protected endpoint
// that 401s without a known bearer token and 200s with one, and a teardown DELETE.
type bootstrapRunSUT struct {
	mu       sync.Mutex
	signups  int
	deleted  []string
	authSeen []string
	known    map[string]bool // bearer tokens the protected endpoint accepts
}

func newBootstrapRunSUT() (*httptest.Server, *bootstrapRunSUT) {
	rec := &bootstrapRunSUT{known: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/signup", func(w http.ResponseWriter, _ *http.Request) {
		rec.mu.Lock()
		rec.signups++
		id := "acct-" + strconv.Itoa(rec.signups)
		tok := "tok-" + id
		rec.known[tok] = true
		rec.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": tok, "id": id})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		rec.mu.Lock()
		rec.authSeen = append(rec.authSeen, auth)
		accept := false
		for tok := range rec.known {
			if auth == "Bearer "+tok {
				accept = true
				break
			}
		}
		rec.mu.Unlock()
		if !accept {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/accounts/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/accounts/"):]
		rec.mu.Lock()
		rec.deleted = append(rec.deleted, id)
		rec.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return httptest.NewServer(mux), rec
}

const bootstrapRunDoc = `target: %s
users: 3
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

// TestRunBootstrapInProcess is a CLI end-to-end: a closed bootstrap run provisions
// one account per virtual user, every protected request authenticates (200, not
// 401), and the accounts are deprovisioned by default.
func TestRunBootstrapInProcess(t *testing.T) {
	sut, rec := newBootstrapRunSUT()
	defer sut.Close()

	file := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(file, []byte(fmt.Sprintf(bootstrapRunDoc, sut.URL)), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	out := captureStdout(t, func() error { return runScenario([]string{file, "--json"}) })
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" || rep.Stats.Total != 3 {
		t.Fatalf("got status=%q total=%d, want completed/3", rep.Run.Status, rep.Stats.Total)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.signups != 3 {
		t.Errorf("provisioned %d accounts, want 3 (one per virtual user)", rec.signups)
	}
	for _, a := range rec.authSeen {
		if a == "Bearer " || a == "" {
			t.Errorf("a protected request was unauthenticated (%q)", a)
		}
	}
	if len(rec.deleted) != 3 {
		t.Errorf("deprovisioned %d accounts, want 3 (default teardown)", len(rec.deleted))
	}
}

// TestRunBootstrapKeepAccounts proves --keep-accounts leaves the accounts in place.
func TestRunBootstrapKeepAccounts(t *testing.T) {
	sut, rec := newBootstrapRunSUT()
	defer sut.Close()

	// No teardown in the doc, so the run relies on --keep-accounts to be accepted.
	doc := fmt.Sprintf(`target: %s
users: 2
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
        extract:
          token: accessToken
          uid: id
    capture:
      token: token
      subject: uid
`, sut.URL)
	file := filepath.Join(t.TempDir(), "keep.yaml")
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	out := captureStdout(t, func() error { return runScenario([]string{file, "--json", "--keep-accounts"}) })
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" {
		t.Fatalf("status=%q, want completed", rep.Run.Status)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.deleted) != 0 {
		t.Errorf("keep-accounts run deprovisioned %d accounts, want 0", len(rec.deleted))
	}
}

// TestRunBootstrapWithoutTeardownRejected proves a no-teardown bootstrap scenario
// without --keep-accounts is refused (gating safety).
func TestRunBootstrapWithoutTeardownRejected(t *testing.T) {
	doc := `target: http://sut.invalid
users: 1
flow:
  - id: a
    request: GET /a
auth:
  strategy: bootstrap-signup
  signup:
    flow:
      - id: register
        request: POST /signup
        extract:
          token: accessToken
    capture:
      token: token
`
	file := filepath.Join(t.TempDir(), "noteardown.yaml")
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	_, err := captureStdoutErr(t, func() error { return runScenario([]string{file}) })
	if err == nil {
		t.Fatal("a no-teardown bootstrap run without --keep-accounts must be rejected")
	}
}

// TestRunBootstrapRejectedAgainstRemoteEngine pins that bootstrap stays in-process
// only — it cannot fan out to a remote engine.
func TestRunBootstrapRejectedAgainstRemoteEngine(t *testing.T) {
	eng := fakeEngine("completed", "")
	defer eng.Close()

	doc := fmt.Sprintf(bootstrapRunDoc, "http://sut.invalid")
	file := filepath.Join(t.TempDir(), "remote.yaml")
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	_, err := captureStdoutErr(t, func() error { return runScenario([]string{file, "--engine", eng.URL}) })
	if err == nil {
		t.Fatal("a bootstrap pool against a remote --engine must be rejected")
	}
}
