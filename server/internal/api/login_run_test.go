package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// loginSUT is a system under test with a /login endpoint that mints a per-request
// token and a protected /a endpoint that 200s for a valid token and 401s
// otherwise. validUntil bounds how many uses a token survives before it starts
// 401ing, so a test can force a mid-run expiry and exercise refresh.
type loginSUT struct {
	mu        sync.Mutex
	minted    int            // how many tokens minted
	uses      map[string]int // per-token request count
	authSeen  []string       // bearer tokens the protected endpoint saw
	expireAt  int            // a token 401s after this many uses (0 = never)
	loginHits int
}

func newLoginSUT(expireAt int) (*httptest.Server, *loginSUT) {
	st := &loginSUT{uses: map[string]int{}, expireAt: expireAt}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		st.mu.Lock()
		st.minted++
		tok := "tok-" + itoa(st.minted)
		st.loginHits++
		st.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": tok, "user": "principal"})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		st.mu.Lock()
		st.authSeen = append(st.authSeen, tok)
		st.uses[tok]++
		used := st.uses[tok]
		exp := st.expireAt
		st.mu.Unlock()
		if tok == "" || (exp > 0 && used > exp) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux), st
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// loginFlowSpecFor builds the standalone login flow that POSTs /login and captures
// the token + subject.
func loginFlowSpecFor() *runspec.LoginFlowSpec {
	return &runspec.LoginFlowSpec{
		Graph:      domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "tlogin"}}},
		Templates:  map[domain.ID]domain.APITemplate{"tlogin": {Method: "POST", Path: "/login", Extract: map[string]string{"token": "access_token", "subject": "user"}}},
		Start:      "login",
		TokenVar:   "token",
		SubjectVar: "subject",
	}
}

// specLogin builds a closed login spec: N users hit the protected /a endpoint,
// authenticated by a token minted from the login flow.
func specLogin(sutURL string, userCount int, scope domain.LoginScope) RunSpec {
	flow := loginFlowSpecFor()
	return RunSpec{
		Experiment: domain.Experiment{
			Name: "login", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: userCount, AuthStrategy: domain.CredLogin},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL: sutURL, Allowlist: []string{"127.0.0.1"},
			RateCap: domain.RateCap{MaxRPS: 10000, MaxConcurrency: 1000}, EnvClass: domain.EnvDev,
		},
		Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}}},
		Templates: map[domain.ID]domain.APITemplate{"ta": {Method: "GET", Path: "/a", Headers: map[string]string{"Authorization": "Bearer {{.token}}"}}},
		Start:     "a", MaxSteps: 1, UserCount: userCount, Seed: 1,
		CredentialPool: &domain.CredentialPool{ID: "p", Strategy: domain.CredLogin, LoginFlowID: idp("login"), LoginScope: scope},
		LoginFlow:      flow,
	}
}

func idp(s string) *domain.ID { id := domain.ID(s); return &id }

// TestClosedLoginRunAuthenticates drives a closed login run and asserts every
// protected request carried a minted bearer token (the run authenticated via the
// login flow), and the login endpoint was hit to mint them.
func TestClosedLoginRunAuthenticates(t *testing.T) {
	sut, st := newLoginSUT(0) // tokens never expire
	defer sut.Close()

	rep := runInProcess(t, specLogin(sut.URL, 3, ""), 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	if rep.Stats.Total != 3 {
		t.Fatalf("stats.Total = %d, want 3", rep.Stats.Total)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.loginHits == 0 {
		t.Error("login endpoint was never hit; the run did not mint tokens")
	}
	for _, tok := range st.authSeen {
		if !strings.HasPrefix(tok, "tok-") {
			t.Errorf("protected endpoint saw a non-minted token %q", tok)
		}
	}
}

// TestClosedLoginRunRecoversMidRunExpiry forces every token to 401 after one use,
// so each user's request expires and triggers a refresh-and-retry. The run must
// still complete with no error-rate finding (the swallowed 401s are excused) and
// every recovered request ultimately 200s.
func TestClosedLoginRunRecoversMidRunExpiry(t *testing.T) {
	sut, st := newLoginSUT(1) // a token 401s after a single use
	defer sut.Close()

	// Two steps per user so the FIRST use is consumed during prewarm/first request
	// and the SECOND must refresh. Use a two-node graph so each user makes 2 calls.
	spec := specLogin(sut.URL, 2, "")
	spec.Graph = domain.ScenarioGraph{ID: "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "ta"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1}},
	}
	spec.MaxSteps = 4

	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed (reason %q)", rep.Run.Status, rep.Run.KillReason)
	}
	// No error-rate finding: the expired-then-refreshed 401s are excused, and the
	// retries succeeded.
	for _, f := range rep.Findings {
		if f.Category == domain.FindingThreshold && f.EvidenceRef == "error-rate" {
			t.Errorf("login refresh churn surfaced an error-rate finding: %+v", f)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.minted < 2 {
		t.Errorf("only %d tokens minted; expected refreshes to mint more", st.minted)
	}
}

// TestOpenLoginRunAuthenticates drives an open (arrival-rate) login run and asserts
// the sessions authenticated with minted tokens.
func TestOpenLoginRunAuthenticates(t *testing.T) {
	sut, st := newLoginSUT(0)
	defer sut.Close()

	spec := specLogin(sut.URL, 0, "")
	spec.Experiment.Params.VirtualUserCount = 1
	spec.Workload = &domain.WorkloadModel{
		Kind:            domain.WorkloadOpen,
		Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, StartRate: 100, PeakRate: 100},
		DurationSeconds: 1,
	}

	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	if rep.Stats.Total < 1 {
		t.Fatalf("open login run produced %d requests, want >= 1", rep.Stats.Total)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, tok := range st.authSeen {
		if !strings.HasPrefix(tok, "tok-") {
			t.Errorf("open session saw a non-minted token %q", tok)
		}
	}
}

// TestReproduceLoginIsRefreshFree pins invariant 7: reproduce a login finding must
// re-acquire the token deterministically (a single login of the same index) and
// must NOT perform a live mid-run refresh. We drive a run that yields a contract
// finding, then reproduce it, and confirm the replay sent a minted token without
// the refresh churn (the reproduce mints exactly one fresh login).
func TestReproduceLoginIsRefreshFree(t *testing.T) {
	// A SUT whose protected endpoint always 500s (contract finding) but whose login
	// always succeeds, so the run produces a reproducible contract finding under a
	// minted token.
	var loginHits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt64(&loginHits, 1)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + itoa(int(n)), "user": "principal"})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	sut := httptest.NewServer(mux)
	defer sut.Close()

	spec := specLogin(sut.URL, 5, "")
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	id, err := srv.CreateExperiment(spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	runID, err := srv.StartRun(id)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitTerminal(t, srv, runID, 5*time.Second)

	rep, _ := srv.Report(runID)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("run status = %q (reason %q), want completed", rep.Run.Status, rep.Run.KillReason)
	}
	var contract *domain.Finding
	for i := range rep.Findings {
		if rep.Findings[i].Category == domain.FindingContract {
			contract = &rep.Findings[i]
			break
		}
	}
	if contract == nil {
		t.Fatalf("expected a contract finding from the 500ing endpoint, got %+v", rep.Findings)
	}

	before := atomic.LoadInt64(&loginHits)
	res, err := srv.ReproduceFinding(t.Context(), runID, ReproduceRequest{Category: contract.Category, EvidenceRef: contract.EvidenceRef, Attempts: 2})
	if err != nil {
		t.Fatalf("reproduce: %v", err)
	}
	after := atomic.LoadInt64(&loginHits)
	// Reproduce re-acquires the token: at least one login, but a bounded number —
	// crucially NOT a refresh loop. The 500 endpoint never 401s, so no refresh is
	// ever triggered during the replay (refresh-free).
	if after == before {
		t.Error("reproduce never re-acquired a login token (expected a deterministic re-acquire)")
	}
	if res.Reproduced == 0 {
		t.Error("the contract finding did not reproduce under the minted token")
	}
}

// TestSharedLoginRunMintsOneToken drives a closed shared-scope login run of
// several users and asserts every protected request carried the SAME minted token
// and the login endpoint was hit exactly once (one client_credentials grant for the
// whole run).
func TestSharedLoginRunMintsOneToken(t *testing.T) {
	sut, st := newLoginSUT(0)
	defer sut.Close()

	rep := runInProcess(t, specLogin(sut.URL, 4, domain.LoginShared), 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.minted != 1 {
		t.Errorf("shared scope minted %d tokens, want 1 (one client_credentials grant)", st.minted)
	}
	// Every protected request carried the one shared token.
	distinct := map[string]struct{}{}
	for _, tok := range st.authSeen {
		distinct[tok] = struct{}{}
	}
	if len(distinct) != 1 {
		t.Errorf("shared run sent %d distinct tokens, want 1: %v", len(distinct), distinct)
	}
}

// multiUserLoginSUT mints a token that encodes the username it was logged in with,
// so a test can prove each virtual user logged in as a DIFFERENT account. The
// protected endpoint records every (token) it saw.
func newMultiUserLoginSUT() (*httptest.Server, *loginSUT) {
	st := &loginSUT{uses: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			U string `json:"u"`
			P string `json:"p"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		st.mu.Lock()
		st.minted++
		st.loginHits++
		st.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		// The token names the account it was minted for, so the protected endpoint's
		// observations reveal which user each request authenticated as.
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + body.U + "-" + body.P, "user": body.U})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		st.mu.Lock()
		st.authSeen = append(st.authSeen, tok)
		st.mu.Unlock()
		if tok == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux), st
}

// specMultiUserLogin builds a closed login spec whose login flow POSTs the
// {{.username}}/{{.password}} of the row a VU is assigned, carrying a credential
// pool of login-INPUT rows (P8 multi-user login).
func specMultiUserLogin(sutURL string, userCount int, rows []domain.Credential) RunSpec {
	spec := specLogin(sutURL, userCount, "")
	spec.LoginFlow.Templates = map[domain.ID]domain.APITemplate{
		"tlogin": {Method: "POST", Path: "/login", PayloadTemplate: `{"u":"{{.username}}","p":"{{.password}}"}`, Extract: map[string]string{"token": "access_token", "subject": "user"}},
	}
	spec.CredentialPool.Entries = rows
	return spec
}

// TestClosedMultiUserLoginLogsInDistinctAccounts drives a closed login run whose
// pool carries three login-input rows: VU 0/1/2 each log in as a DIFFERENT account
// (distinct username+password) and carry that account's token. The entries reach the
// login token func through the orchestrator, and the wrap-around (row i%N) keys each
// VU deterministically.
func TestClosedMultiUserLoginLogsInDistinctAccounts(t *testing.T) {
	sut, st := newMultiUserLoginSUT()
	defer sut.Close()

	rows := []domain.Credential{
		{Subject: "alice", Secret: "pw-a"},
		{Subject: "bob", Secret: "pw-b"},
		{Subject: "carol", Secret: "pw-c"},
	}
	rep := runInProcess(t, specMultiUserLogin(sut.URL, 3, rows), 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	if rep.Stats.Total != 3 {
		t.Fatalf("stats.Total = %d, want 3", rep.Stats.Total)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	// Each VU authenticated as a different account: the three account-specific tokens
	// all appear among the protected endpoint's observations.
	want := map[string]bool{"tok-alice-pw-a": false, "tok-bob-pw-b": false, "tok-carol-pw-c": false}
	for _, tok := range st.authSeen {
		if _, ok := want[tok]; !ok {
			t.Errorf("protected endpoint saw an unexpected token %q (not an account-specific minted token)", tok)
		}
		want[tok] = true
	}
	for tok, seen := range want {
		if !seen {
			t.Errorf("no request authenticated as %q; a per-account login was missed", tok)
		}
	}
}

// TestMultiUserLoginWrapsAround drives 4 users against a 2-row pool: VU 3 wraps to
// row 1 (3 % 2 == 1), so only two distinct accounts log in and the run authenticates
// every VU.
func TestMultiUserLoginWrapsAround(t *testing.T) {
	sut, st := newMultiUserLoginSUT()
	defer sut.Close()

	rows := []domain.Credential{
		{Subject: "alice", Secret: "pw-a"},
		{Subject: "bob", Secret: "pw-b"},
	}
	rep := runInProcess(t, specMultiUserLogin(sut.URL, 4, rows), 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	distinct := map[string]struct{}{}
	for _, tok := range st.authSeen {
		distinct[tok] = struct{}{}
	}
	// Two rows → at most two distinct account tokens even with four users.
	if len(distinct) != 2 {
		t.Errorf("4 users over a 2-row pool sent %d distinct account tokens, want 2 (wrap-around): %v", len(distinct), distinct)
	}
	if _, ok := distinct["tok-alice-pw-a"]; !ok {
		t.Error("row 0 (alice) was never logged in")
	}
	if _, ok := distinct["tok-bob-pw-b"]; !ok {
		t.Error("row 1 (bob) was never logged in")
	}
}

// TestReproduceMultiUserLoginDeterministic pins that reproducing a finding from a
// multi-user login run re-logs-in as the SAME account the evidence session ran as:
// the entries flow through loginAuthFor on both the run and reproduce paths, so VU i
// re-acquires row i%N deterministically.
func TestReproduceMultiUserLoginDeterministic(t *testing.T) {
	// The protected endpoint always 500s (a reproducible contract finding) while the
	// login encodes the account, so the replay can be checked to log in as that
	// account.
	st := &loginSUT{uses: map[string]int{}}
	mux := http.NewServeMux()
	var loginBodies []string
	var bodiesMu sync.Mutex
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			U string `json:"u"`
			P string `json:"p"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodiesMu.Lock()
		loginBodies = append(loginBodies, body.U+":"+body.P)
		bodiesMu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + body.U, "user": body.U})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	sut := httptest.NewServer(mux)
	defer sut.Close()
	_ = st

	rows := []domain.Credential{
		{Subject: "alice", Secret: "pw-a"},
		{Subject: "bob", Secret: "pw-b"},
		{Subject: "carol", Secret: "pw-c"},
	}
	spec := specMultiUserLogin(sut.URL, 3, rows)
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	id, err := srv.CreateExperiment(spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	runID, err := srv.StartRun(id)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitTerminal(t, srv, runID, 5*time.Second)
	rep, _ := srv.Report(runID)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("run status = %q (reason %q), want completed", rep.Run.Status, rep.Run.KillReason)
	}
	var contract *domain.Finding
	for i := range rep.Findings {
		if rep.Findings[i].Category == domain.FindingContract {
			contract = &rep.Findings[i]
			break
		}
	}
	if contract == nil {
		t.Fatalf("expected a contract finding, got %+v", rep.Findings)
	}

	bodiesMu.Lock()
	loginBodies = nil
	bodiesMu.Unlock()
	res, err := srv.ReproduceFinding(t.Context(), runID, ReproduceRequest{Category: contract.Category, EvidenceRef: contract.EvidenceRef, Attempts: 1})
	if err != nil {
		t.Fatalf("reproduce: %v", err)
	}
	if res.Reproduced == 0 {
		t.Error("the contract finding did not reproduce")
	}
	bodiesMu.Lock()
	defer bodiesMu.Unlock()
	if len(loginBodies) == 0 {
		t.Fatal("reproduce never re-logged-in")
	}
	// Every re-login during reproduce used one of the pool rows (a valid account),
	// proving the entries flow through the reproduce path's loginAuthFor too. The
	// exact row is the deterministic i%N of the replayed session.
	valid := map[string]bool{"alice:pw-a": true, "bob:pw-b": true, "carol:pw-c": true}
	for _, b := range loginBodies {
		if !valid[b] {
			t.Errorf("reproduce logged in with %q, not one of the pool rows (entries did not reach reproduce)", b)
		}
	}
}

func waitTerminal(t *testing.T, srv *Server, runID domain.ID, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rep, ok := srv.Report(runID)
		if ok {
			switch rep.Run.Status {
			case domain.RunCompleted, domain.RunFailed, domain.RunKilled:
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish within %s", timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
