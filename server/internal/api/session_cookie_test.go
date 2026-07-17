package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// TestSessionCookieLoginRoundTrip proves the session-cookie auth route works
// end to end WITHOUT any new server capability: a login whose response carries
// only a Set-Cookie (no body token) has that cookie auto-captured as the
// credential secret (load.DetectCredential's cookie fallback), and the scenario
// replays it as a "Cookie: session={{.token}}" header — exactly the shape the
// importer's apiKey-in-cookie derivation emits. The protected endpoint accepts
// only the captured session value.
func TestSessionCookieLoginRoundTrip(t *testing.T) {
	var mu sync.Mutex
	issued := map[string]bool{}
	unauthorized := 0
	seq := 0
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			// Respond with ONLY a session cookie — no token in the body.
			mu.Lock()
			seq++
			sid := "sess-" + itoa(seq)
			issued[sid] = true
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "session", Value: sid, Path: "/"})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			// The protected endpoint accepts only a live issued session cookie.
			c, err := r.Cookie("session")
			mu.Lock()
			ok := err == nil && issued[c.Value]
			if !ok {
				unauthorized++
			}
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer sut.Close()

	flowID := domain.ID("login")
	spec := specFor(sut.URL, 3)
	spec.Graph = domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}}}
	spec.Templates = map[domain.ID]domain.APITemplate{
		// The scenario replays the captured session value as a Cookie header — the
		// exact shape deriveAPIKey emits for an apiKey-in-cookie scheme.
		"ta": {Method: "GET", Path: "/data", Headers: map[string]string{"Cookie": "session={{.token}}"}},
	}
	spec.Start = "a"
	spec.MaxSteps = 1
	spec.Experiment.Params.AuthStrategy = domain.CredLogin
	spec.CredentialPool = &domain.CredentialPool{ID: "p", Strategy: domain.CredLogin, LoginFlowID: &flowID}
	spec.LoginFlow = &runspec.LoginFlowSpec{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t_login"}}},
		Templates: map[domain.ID]domain.APITemplate{"t_login": {Method: "POST", Path: "/login"}},
		Start:     "login",
		MaxSteps:  1,
		// No TokenVar: the token is auto-detected from the response — here from the
		// Set-Cookie, since the body carries none.
	}

	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q (reason %q), want completed", rep.Run.Status, rep.Run.KillReason)
	}
	if rep.Stats.Errors != 0 {
		t.Errorf("stats.Errors = %d, want 0 (the captured session cookie must authenticate every request)", rep.Stats.Errors)
	}
	mu.Lock()
	defer mu.Unlock()
	if unauthorized != 0 {
		t.Errorf("SUT saw %d unauthorized requests, want 0 — the session cookie was not captured/replayed", unauthorized)
	}
}

// TestSessionCookieReloginOnExpiry proves the expiry path: when a session cookie
// is rejected mid-run (the server expires it after one use), the LoginProvider's
// re-login fallback mints a fresh session and the retry succeeds — so a static
// session pool self-heals exactly like a token pool, with no refresh transport.
func TestSessionCookieReloginOnExpiry(t *testing.T) {
	var mu sync.Mutex
	live := map[string]bool{} // sessions that have not yet been consumed
	seq := 0
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			mu.Lock()
			seq++
			sid := "sess-" + itoa(seq)
			live[sid] = true
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "session", Value: sid, Path: "/"})
			w.WriteHeader(http.StatusOK)
		default:
			c, err := r.Cookie("session")
			mu.Lock()
			ok := err == nil && live[c.Value]
			if ok {
				delete(live, c.Value) // single-use: expire after one request
			}
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusUnauthorized) // triggers re-login + one retry
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer sut.Close()

	flowID := domain.ID("login")
	spec := specFor(sut.URL, 1)
	// Two steps so the second use forces a re-login after the first consumed the cookie.
	spec.Graph = domain.ScenarioGraph{ID: "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "ta"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1}},
	}
	spec.Templates = map[domain.ID]domain.APITemplate{
		"ta": {Method: "GET", Path: "/data", Headers: map[string]string{"Cookie": "session={{.token}}"}},
	}
	spec.Start = "a"
	spec.MaxSteps = 4
	spec.Experiment.Params.AuthStrategy = domain.CredLogin
	spec.CredentialPool = &domain.CredentialPool{ID: "p", Strategy: domain.CredLogin, LoginFlowID: &flowID}
	spec.LoginFlow = &runspec.LoginFlowSpec{
		Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t_login"}}},
		Templates: map[domain.ID]domain.APITemplate{"t_login": {Method: "POST", Path: "/login"}},
		Start:     "login",
		MaxSteps:  1,
	}

	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q (reason %q), want completed", rep.Run.Status, rep.Run.KillReason)
	}
	// The re-login recovery must have minted more than one session.
	mu.Lock()
	minted := seq
	mu.Unlock()
	if minted < 2 {
		t.Errorf("sessions minted = %d, want >= 2 (a mid-run 401 must trigger a re-login)", minted)
	}
	for _, f := range rep.Findings {
		if f.Category == domain.FindingThreshold && strings.Contains(f.EvidenceRef, "error-rate") {
			t.Errorf("session re-login recovery surfaced an error-rate finding: %+v", f)
		}
	}
}
