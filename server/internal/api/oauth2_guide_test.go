package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// oauth2GuideIdP is a fake OAuth2 token endpoint speaking the three grants the
// web OAuth2 guide mode generates (password, client_credentials, refresh_token),
// plus a protected /api/data that 401s any request without a live access token.
// It counts grants and unauthorized hits so a test can assert 401 == 0 and that
// the expected grant reached the wire.
type oauth2GuideIdP struct {
	mu           sync.Mutex
	grants       map[string]int    // grant_type -> count
	tokens       map[string]bool   // live access tokens
	refresh      map[string]bool   // valid refresh tokens
	unauthorized int               // /api/data hits without a live token
	users        map[string]string // username -> password
	minted       int
}

func newOAuth2GuideIdP(users map[string]string, pastedRefresh string) (*httptest.Server, *oauth2GuideIdP) {
	st := &oauth2GuideIdP{
		grants:  map[string]int{},
		tokens:  map[string]bool{},
		refresh: map[string]bool{},
		users:   users,
	}
	if pastedRefresh != "" {
		st.refresh[pastedRefresh] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		st.mu.Lock()
		defer st.mu.Unlock()
		grant := r.PostFormValue("grant_type")
		st.grants[grant]++
		switch grant {
		case "password":
			if st.users[r.PostFormValue("username")] != r.PostFormValue("password") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		case "client_credentials":
			if r.PostFormValue("client_id") != "web" || r.PostFormValue("client_secret") != "shh" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		case "refresh_token":
			if !st.refresh[r.PostFormValue("refresh_token")] {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		st.minted++
		access := "acc-" + itoa(st.minted)
		next := "ref-" + itoa(st.minted)
		st.tokens[access] = true
		st.refresh[next] = true
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": access, "refresh_token": next, "token_type": "Bearer", "expires_in": 3600,
		})
	})
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		tok, ok := trimBearer(r.Header.Get("Authorization"))
		if !ok || !st.tokens[tok] {
			st.unauthorized++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return httptest.NewServer(mux), st
}

func trimBearer(h string) (string, bool) {
	const p = "Bearer "
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):], true
	}
	return "", false
}

// specOAuth2Guide is the RunSpec shape the web OAuth2 guide mode compiles a form
// into: a login-strategy pool whose single-step flow POSTs the token endpoint
// with the given form body, and a one-node scenario hitting the protected
// endpoint with the minted bearer token.
func specOAuth2Guide(sutURL, body string, scope domain.LoginScope, entries []domain.Credential, userCount int) RunSpec {
	spec := RunSpec{
		Experiment: domain.Experiment{
			Name: "oauth2-guide", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: userCount, AuthStrategy: domain.CredLogin},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL: sutURL, Allowlist: []string{"127.0.0.1"},
			RateCap: domain.RateCap{MaxRPS: 10000, MaxConcurrency: 1000}, EnvClass: domain.EnvDev,
		},
		Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}}},
		Templates: map[domain.ID]domain.APITemplate{"ta": {Method: "GET", Path: "/api/data", Headers: map[string]string{"Authorization": "Bearer {{.token}}"}}},
		Start:     "a", MaxSteps: 1, UserCount: userCount, Seed: 1,
		CredentialPool: &domain.CredentialPool{
			ID: "web-pool", Strategy: domain.CredLogin, LoginFlowID: idp("login"),
			LoginScope: scope, Entries: entries,
		},
		LoginFlow: &runspec.LoginFlowSpec{
			Graph: domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t_login"}}},
			Templates: map[domain.ID]domain.APITemplate{"t_login": {
				Method:          "POST",
				Path:            "/oauth/token",
				Headers:         map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
				PayloadTemplate: body,
			}},
			Start: "login",
		},
	}
	return spec
}

// TestOAuth2GuidePasswordGrantRun drives the guide's "log in with a username and
// password" path end to end: per-user rows log in via grant_type=password and
// every protected request carries a live token — 401 == 0.
func TestOAuth2GuidePasswordGrantRun(t *testing.T) {
	sut, st := newOAuth2GuideIdP(map[string]string{"alice": "pw-a", "bob": "pw-b"}, "")
	defer sut.Close()

	spec := specOAuth2Guide(sut.URL,
		"grant_type=password&username={{.username}}&password={{.password}}&client_id=web&scope=read",
		domain.LoginPerUser,
		[]domain.Credential{{Subject: "alice", Secret: "pw-a"}, {Subject: "bob", Secret: "pw-b"}},
		2)
	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q (reason %q), want completed", rep.Run.Status, rep.Run.KillReason)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.unauthorized != 0 {
		t.Errorf("unauthorized hits = %d, want 0", st.unauthorized)
	}
	if st.grants["password"] < 2 {
		t.Errorf("password grants = %d, want >= 2 (one per user)", st.grants["password"])
	}
}

// TestOAuth2GuideClientCredentialsRun drives the guide's "log in with a client
// key (server-to-server)" path: one shared client_credentials grant serves every
// virtual user — 401 == 0 and exactly one mint.
func TestOAuth2GuideClientCredentialsRun(t *testing.T) {
	sut, st := newOAuth2GuideIdP(nil, "")
	defer sut.Close()

	spec := specOAuth2Guide(sut.URL,
		"grant_type=client_credentials&client_id=web&client_secret=shh&scope=read",
		domain.LoginShared, nil, 3)
	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q (reason %q), want completed", rep.Run.Status, rep.Run.KillReason)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.unauthorized != 0 {
		t.Errorf("unauthorized hits = %d, want 0", st.unauthorized)
	}
	if st.grants["client_credentials"] != 1 {
		t.Errorf("client_credentials grants = %d, want exactly 1 (shared scope)", st.grants["client_credentials"])
	}
}

// TestOAuth2GuideRefreshTokenPasteRun drives the guide's "already logged in on an
// app/browser" path: the login flow is ITSELF a grant_type=refresh_token exchange
// starting from a pasted refresh token, shared across users — 401 == 0.
func TestOAuth2GuideRefreshTokenPasteRun(t *testing.T) {
	sut, st := newOAuth2GuideIdP(nil, "PASTED_REFRESH")
	defer sut.Close()

	spec := specOAuth2Guide(sut.URL,
		"grant_type=refresh_token&refresh_token=PASTED_REFRESH&client_id=web",
		domain.LoginShared, nil, 2)
	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q (reason %q), want completed", rep.Run.Status, rep.Run.KillReason)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.unauthorized != 0 {
		t.Errorf("unauthorized hits = %d, want 0", st.unauthorized)
	}
	if st.grants["refresh_token"] < 1 {
		t.Errorf("refresh_token grants = %d, want >= 1", st.grants["refresh_token"])
	}
}
