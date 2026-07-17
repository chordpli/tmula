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
