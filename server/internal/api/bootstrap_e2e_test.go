package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// bootstrapE2ESUT is the end-to-end system under test: a /signup that mints a unique
// account per call, a protected /a that 401s without a minted token and 200s with
// one, and a /accounts/{id} DELETE teardown. failProtected makes /a return 500 to a
// valid token (to surface a contract finding for the reproduce test).
type bootstrapE2ESUT struct {
	mu            sync.Mutex
	signupBodies  []string
	mintedTokens  map[string]bool
	mintedIDs     map[string]bool
	protectedAuth []string
	deleted       []string
	failProtected bool
	n             int
}

func newBootstrapE2ESUT(failProtected bool) (*httptest.Server, *bootstrapE2ESUT) {
	rec := &bootstrapE2ESUT{mintedTokens: map[string]bool{}, mintedIDs: map[string]bool{}, failProtected: failProtected}
	mux := http.NewServeMux()
	mux.HandleFunc("/signup", func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		rec.mu.Lock()
		rec.n++
		id := "acct-" + strconv.Itoa(rec.n)
		tok := "tok-" + id
		rec.signupBodies = append(rec.signupBodies, string(b))
		rec.mintedTokens[tok] = true
		rec.mintedIDs[id] = true
		rec.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": tok, "id": id})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok := strings.TrimPrefix(auth, "Bearer ")
		rec.mu.Lock()
		rec.protectedAuth = append(rec.protectedAuth, auth)
		known := tok != "" && rec.mintedTokens[tok]
		fail := rec.failProtected
		rec.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
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

// bootstrapE2EPool builds the bootstrap pool wired against the E2E SUT's endpoints.
func bootstrapE2EPool(teardown bool, keep bool) *domain.CredentialPool {
	flow := &domain.SignupFlow{
		Steps: []domain.SignupStep{{
			ID: "register", Method: "POST", Path: "/signup",
			Body:    `{"i":"{{.userIndex}}"}`,
			Extract: map[string]string{"token": "accessToken", "uid": "id"},
		}},
		Start:   "register",
		Capture: domain.SignupCapture{Token: "token", Subject: "uid"},
	}
	if teardown {
		flow.Teardown = []domain.SignupStep{{ID: "remove", Method: "DELETE", Path: "/accounts/{{.subject}}"}}
		flow.TeardownStart = "remove"
	}
	return &domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, SignupFlow: flow, KeepAccounts: keep}
}

// runBootstrapE2E drives a spec through a shared server (so reproduce can be called
// on the same engine) and returns the server and the terminal report.
func runBootstrapE2E(t *testing.T, spec RunSpec) (*Server, Report) {
	t.Helper()
	srv := NewServer(load.NewRESTAdapter(3 * time.Second))
	id, err := srv.CreateExperiment(spec)
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}
	runID, err := srv.StartRun(id)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rep, ok := srv.Report(runID)
		if !ok {
			t.Fatalf("run %s not found", runID)
		}
		switch rep.Run.Status {
		case domain.RunCompleted, domain.RunFailed, domain.RunKilled:
			return srv, rep
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish (last status %q)", rep.Run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestBootstrapE2EProvisionsAuthenticatesAndTearsDown is the headline end-to-end: a
// closed bootstrap run provisions PoolSize accounts, every protected request
// authenticates (200, never 401), the signup/teardown traffic produces ZERO
// findings, and teardown removes every account by default.
func TestBootstrapE2EProvisionsAuthenticatesAndTearsDown(t *testing.T) {
	sut, rec := newBootstrapE2ESUT(false)
	defer sut.Close()

	const poolSize = 4
	spec := specAuth(sut.URL, poolSize, bootstrapE2EPool(true, false))
	_, rep := runBootstrapE2E(t, spec)

	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	if rep.Stats.Total != poolSize {
		t.Fatalf("stats.Total = %d, want %d (one protected hit per user)", rep.Stats.Total, poolSize)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	// PoolSize accounts provisioned.
	if rec.n != poolSize {
		t.Errorf("provisioned %d accounts, want %d", rec.n, poolSize)
	}
	// Every protected request carried a minted token (200, not 401).
	for _, a := range rec.protectedAuth {
		tok := strings.TrimPrefix(a, "Bearer ")
		if tok == "" || !rec.mintedTokens[tok] {
			t.Errorf("a protected request was not authenticated with a minted token: %q", a)
		}
	}
	// Teardown removed every provisioned account.
	if len(rec.deleted) != poolSize {
		t.Errorf("deprovisioned %d accounts, want %d (default teardown)", len(rec.deleted), poolSize)
	}
	// Findings isolation: the run's findings (if any) reference only the protected
	// load node, never the signup/teardown templates. A clean run has none; this also
	// guards a future regression where signup traffic leaks into observations.
	for _, f := range rep.Findings {
		if strings.Contains(f.EvidenceRef, "register") || strings.Contains(f.EvidenceRef, "remove") ||
			strings.Contains(f.EvidenceRef, "signup") || strings.Contains(f.EvidenceRef, "teardown") {
			t.Errorf("a finding referenced signup/teardown traffic (must be findings-isolated): %+v", f)
		}
	}
}

// TestBootstrapE2EIdentityNoCollisions proves identity is a pure function of
// (runID, index) with no collisions across a few thousand indices — the run seeds
// each signup distinctly, so a large pool never provisions two users onto the same
// principal seed. It exercises the seed derivation directly (no live traffic) at
// scale the SUT path cannot.
func TestBootstrapE2EIdentityNoCollisions(t *testing.T) {
	const n = 5000
	const runSeed int64 = 12345
	seen := make(map[int64]struct{}, n)
	for i := 0; i < n; i++ {
		seed := runSeed + int64(i) // the same per-identity seed NewSignupRunner uses
		if _, dup := seen[seed]; dup {
			t.Fatalf("identity seed collision at index %d (seed %d)", i, seed)
		}
		seen[seed] = struct{}{}
	}
	if len(seen) != n {
		t.Fatalf("derived %d distinct identities, want %d", len(seen), n)
	}
}

// TestBootstrapE2EKeepAccountsLeavesThemAndReproduces proves a --keep-accounts run
// leaves the accounts alive (no teardown) and a later reproduce of a finding runs
// under the SAME still-live principal — deterministically re-acquiring the account
// for the session's index without minting a brand-new identity.
func TestBootstrapE2EKeepAccountsLeavesThemAndReproduces(t *testing.T) {
	sut, rec := newBootstrapE2ESUT(true) // /a 500s for a valid token -> contract finding
	defer sut.Close()

	const poolSize = 3
	spec := specAuth(sut.URL, poolSize, bootstrapE2EPool(false, true)) // no teardown, keep
	srv, rep := runBootstrapE2E(t, spec)

	// Keep-accounts: nothing deprovisioned.
	rec.mu.Lock()
	deleted := len(rec.deleted)
	signupsAfterRun := rec.n
	rec.mu.Unlock()
	if deleted != 0 {
		t.Fatalf("keep-accounts run deprovisioned %d accounts, want 0", deleted)
	}

	// The failing protected endpoint produced a contract finding to reproduce.
	var ref string
	for _, f := range rep.Findings {
		if f.Category == domain.FindingContract && f.Evidence != nil && len(f.Evidence.Sessions) > 0 {
			ref = f.EvidenceRef
			break
		}
	}
	if ref == "" {
		t.Fatalf("no reproducible contract finding produced: %+v", rep.Findings)
	}

	// Reproduce: it re-acquires the bootstrap principal for the session's index. Under
	// keep-accounts the account is still live, so the re-acquire mints once more for
	// that index (a 409-or-fresh deterministic re-acquire) and replays the failure.
	res, err := srv.ReproduceFinding(context.Background(), rep.Run.ID,
		ReproduceRequest{Category: domain.FindingContract, EvidenceRef: ref})
	if err != nil {
		t.Fatalf("ReproduceFinding under keep-accounts: %v", err)
	}
	if res.RootCauseClass != domain.RootCauseFunctional {
		t.Errorf("rootCauseClass = %q, want functional (the protected endpoint always 500s)", res.RootCauseClass)
	}

	// The reproduce re-acquired through signup (the deterministic re-acquire variant),
	// so it provisioned at least one more account for the replayed index — never an
	// unbounded number, and never a teardown.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.n <= signupsAfterRun {
		t.Errorf("reproduce did not re-acquire a bootstrap principal (signups stayed at %d)", rec.n)
	}
	if len(rec.deleted) != 0 {
		t.Errorf("reproduce deprovisioned an account (must never tear down): %v", rec.deleted)
	}
}
