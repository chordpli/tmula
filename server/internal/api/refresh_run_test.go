package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// oauthSUT is a system under test exposing a single /oauth/token endpoint that
// handles BOTH the password grant (login) and the refresh_token grant (mid-run
// refresh), plus a protected /a endpoint. It counts each grant type so a test can
// prove a real grant_type=refresh_token exchange fired (or did not).
type oauthSUT struct {
	mu           sync.Mutex
	passwordHits int            // grant_type=password (login)
	refreshHits  int            // grant_type=refresh_token (mid-run refresh)
	minted       int            // total access tokens minted
	uses         map[string]int // per-access-token request count
	expireAt     int            // an access token 401s after this many uses (0 = never)
	authSeen     []string
}

func newOAuthSUT(expireAt int) (*httptest.Server, *oauthSUT) {
	st := &oauthSUT{uses: map[string]int{}, expireAt: expireAt}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		grant := r.PostFormValue("grant_type")
		st.mu.Lock()
		switch grant {
		case "refresh_token":
			st.refreshHits++
		default:
			st.passwordHits++
		}
		st.minted++
		access := "acc-" + itoa(st.minted)
		refresh := "ref-" + itoa(st.minted)
		st.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"expires_in":    3600,
			"user":          "principal",
		})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
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

func bearer(r *http.Request) string {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

// oauthLoginFlowSpec builds the standalone OAuth2 password-grant login flow used by
// the run specs below: a single form-encoded POST to /oauth/token carrying
// grant_type=password, with auto-detected token/refresh capture.
func oauthLoginFlowSpec() *runspec.LoginFlowSpec {
	return &runspec.LoginFlowSpec{
		Graph: domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "token", APITemplateID: "ttoken"}}},
		Templates: map[domain.ID]domain.APITemplate{
			"ttoken": {
				Method:          "POST",
				Path:            "/oauth/token",
				Headers:         map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
				PayloadTemplate: "grant_type=password&username=u&password=p&client_id=c&client_secret=s&scope=read",
			},
		},
		Start: "token",
	}
}

// specOAuthLogin builds a closed login run whose login flow is the OAuth2 password
// grant, so the refresh template is auto-derivable and a mid-run 401 triggers a real
// grant_type=refresh_token exchange.
func specOAuthLogin(sutURL string, userCount int) RunSpec {
	flow := oauthLoginFlowSpec()
	return RunSpec{
		Experiment: domain.Experiment{
			Name: "oauth", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: userCount, AuthStrategy: domain.CredLogin},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL: sutURL, Allowlist: []string{"127.0.0.1"},
			RateCap: domain.RateCap{MaxRPS: 10000, MaxConcurrency: 1000}, EnvClass: domain.EnvDev,
		},
		Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}}},
		Templates: map[domain.ID]domain.APITemplate{"ta": {Method: "GET", Path: "/a", Headers: map[string]string{"Authorization": "Bearer {{.token}}"}}},
		Start:     "a", MaxSteps: 1, UserCount: userCount, Seed: 1,
		CredentialPool: &domain.CredentialPool{ID: "p", Strategy: domain.CredLogin, LoginFlowID: idp("login")},
		LoginFlow:      flow,
	}
}

// TestOAuthRefreshGrantFiresMidRun pins test 8: an OAuth2 login run whose access
// token 401s mid-run recovers via a REAL grant_type=refresh_token exchange (not a
// re-login), retries once and 200s, and surfaces no error-rate finding. The token
// endpoint must see at least one refresh_token grant.
func TestOAuthRefreshGrantFiresMidRun(t *testing.T) {
	sut, st := newOAuthSUT(1) // each access token 401s after a single use
	defer sut.Close()

	// Two steps per user: the first use consumes the access token, the second must
	// refresh via the refresh_token grant.
	spec := specOAuthLogin(sut.URL, 2)
	spec.Graph = domain.ScenarioGraph{ID: "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "ta"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1}},
	}
	spec.MaxSteps = 4

	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed (reason %q)", rep.Run.Status, rep.Run.KillReason)
	}
	for _, f := range rep.Findings {
		if f.Category == domain.FindingThreshold && f.EvidenceRef == "error-rate" {
			t.Errorf("refresh-grant recovery surfaced an error-rate finding: %+v", f)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.refreshHits == 0 {
		t.Errorf("no grant_type=refresh_token exchange fired; password=%d refresh=%d (expected a real refresh grant on mid-run 401)", st.passwordHits, st.refreshHits)
	}
}

// TestReproduceOAuthLoginIsRefreshFree pins invariant 7 for the derivable-refresh
// case: even when the login flow DOES derive a refresh template (so the run path
// wires a RefreshTokenFunc), reproducing a login finding performs ZERO
// grant_type=refresh_token POSTs — the replay re-acquires via Acquire only and never
// refreshes.
func TestReproduceOAuthLoginIsRefreshFree(t *testing.T) {
	// The protected endpoint always 500s (a reproducible contract finding) while the
	// token endpoint always succeeds, so the run yields a contract finding under a
	// minted access token and the replay re-acquires deterministically.
	st := &oauthSUT{uses: map[string]int{}}
	mux := http.NewServeMux()
	var refreshHits int64
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostFormValue("grant_type") == "refresh_token" {
			atomic.AddInt64(&refreshHits, 1)
		}
		st.mu.Lock()
		st.minted++
		n := st.minted
		st.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acc-" + itoa(n), "refresh_token": "ref-" + itoa(n), "expires_in": 3600, "user": "principal",
		})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	sut := httptest.NewServer(mux)
	defer sut.Close()

	spec := specOAuthLogin(sut.URL, 5)
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

	atomic.StoreInt64(&refreshHits, 0)
	res, err := srv.ReproduceFinding(t.Context(), runID, ReproduceRequest{Category: contract.Category, EvidenceRef: contract.EvidenceRef, Attempts: 2})
	if err != nil {
		t.Fatalf("reproduce: %v", err)
	}
	if res.Reproduced == 0 {
		t.Error("the contract finding did not reproduce under the minted token")
	}
	if got := atomic.LoadInt64(&refreshHits); got != 0 {
		t.Errorf("reproduce performed %d grant_type=refresh_token POSTs, want 0 (reproduce must stay refresh-free)", got)
	}
}
