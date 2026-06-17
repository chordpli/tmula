package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
)

// oneNodeAuth builds a single-request flow whose template echoes the bearer token,
// so a SUT can answer 401 or 200 based on the token it sees.
func oneNodeAuth() (domain.ScenarioGraph, map[domain.ID]domain.APITemplate) {
	g := domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}}}
	tmpls := map[domain.ID]domain.APITemplate{
		"ta": {Method: "GET", Path: "/a", Headers: map[string]string{"Authorization": "Bearer {{.token}}"}},
	}
	return g, tmpls
}

// TestRefreshRecovers401EmitsOneObservation is the findings-isolation invariant for
// refresh: a step that 401s with a refresher available is refreshed and retried
// once; when the retry succeeds, exactly ONE result is emitted (the successful
// retry) and the swallowed 401 is never observed.
func TestRefreshRecovers401EmitsOneObservation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A request bearing the fresh token succeeds; the stale token 401s.
		if r.Header.Get("Authorization") == "Bearer fresh" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	g, tmpls := oneNodeAuth()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	holder := NewCredentialHolder(domain.Credential{Secret: "stale"})
	var refreshes int64
	u := VirtualUser{
		ID:     "u0",
		Holder: holder,
		Refresh: func(context.Context) error {
			atomic.AddInt64(&refreshes, 1)
			holder.Set(domain.Credential{Secret: "fresh"})
			return nil
		},
	}
	results, err := r.RunSession(context.Background(), g, "a", 5, u, 1, nil)
	if err != nil {
		t.Fatalf("run session: %v", err)
	}
	if n := atomic.LoadInt64(&refreshes); n != 1 {
		t.Errorf("refresh ran %d times, want 1", n)
	}
	if len(results) != 1 {
		t.Fatalf("recovered 401 emitted %d results, want exactly 1 (the successful retry)", len(results))
	}
	res := results[0]
	if res.Resp.StatusCode != http.StatusOK {
		t.Errorf("emitted result status = %d, want 200 (the retry)", res.Resp.StatusCode)
	}
	if res.ErrorClass != "" {
		t.Errorf("recovered result carries class %q, want empty (it succeeded)", res.ErrorClass)
	}
}

// TestRefreshExhaustedTags401AuthRefresh covers the exhausted path: the retry still
// 401s (the refresh did not recover), so exactly one result is emitted and it
// carries the auth-refresh class, which makes obs.failed() ignore it while a plain
// 401 stays a failure.
func TestRefreshExhaustedTags401AuthRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // always 401: refresh never recovers
	}))
	defer srv.Close()

	g, tmpls := oneNodeAuth()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	holder := NewCredentialHolder(domain.Credential{Secret: "stale"})
	var refreshes int64
	u := VirtualUser{
		ID:     "u0",
		Holder: holder,
		Refresh: func(context.Context) error {
			atomic.AddInt64(&refreshes, 1)
			holder.Set(domain.Credential{Secret: "still-bad"})
			return nil
		},
	}
	results, err := r.RunSession(context.Background(), g, "a", 5, u, 1, nil)
	if err != nil {
		t.Fatalf("run session: %v", err)
	}
	if n := atomic.LoadInt64(&refreshes); n != 1 {
		t.Errorf("refresh ran %d times, want exactly 1 (retry-once, no loop)", n)
	}
	if len(results) != 1 {
		t.Fatalf("exhausted 401 emitted %d results, want exactly 1", len(results))
	}
	res := results[0]
	if res.Resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("emitted status = %d, want 401", res.Resp.StatusCode)
	}
	if res.ErrorClass != obs.ErrorClassAuthRefresh {
		t.Errorf("exhausted 401 class = %q, want %q", res.ErrorClass, obs.ErrorClassAuthRefresh)
	}
}

// TestPlain401WithoutRefresherStaysFailure proves a 401 with NO refresher is
// untouched: no retry, no auth-refresh class — it is a plain failure exactly as
// before this path existed.
func TestPlain401WithoutRefresherStaysFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	g, tmpls := oneNodeAuth()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	// No Holder, no Refresh: the static path.
	u := VirtualUser{ID: "u0", Cred: domain.Credential{Secret: "tok"}}
	results, err := r.RunSession(context.Background(), g, "a", 5, u, 1, nil)
	if err != nil {
		t.Fatalf("run session: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("plain 401 emitted %d results, want 1", len(results))
	}
	if results[0].ErrorClass != "" {
		t.Errorf("plain 401 carries class %q, want empty (no refresher)", results[0].ErrorClass)
	}
	if results[0].Resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("plain 401 status = %d, want 401", results[0].Resp.StatusCode)
	}
}

// TestRefreshFailureTags401AuthRefresh covers a refresh that itself errors (the
// login endpoint is down): the original 401 is emitted once, tagged auth-refresh,
// and no retry is sent.
func TestRefreshFailureTags401AuthRefresh(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	g, tmpls := oneNodeAuth()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)
	holder := NewCredentialHolder(domain.Credential{Secret: "stale"})
	u := VirtualUser{
		ID:      "u0",
		Holder:  holder,
		Refresh: func(context.Context) error { return context.DeadlineExceeded },
	}
	results, err := r.RunSession(context.Background(), g, "a", 5, u, 1, nil)
	if err != nil {
		t.Fatalf("run session: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("failed-refresh 401 emitted %d results, want 1", len(results))
	}
	if results[0].ErrorClass != obs.ErrorClassAuthRefresh {
		t.Errorf("failed-refresh 401 class = %q, want %q", results[0].ErrorClass, obs.ErrorClassAuthRefresh)
	}
	// One original request, no retry (refresh failed before any retry).
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Errorf("server hit %d times, want 1 (no retry when refresh fails)", n)
	}
}

// TestRecovered401NotObservedThroughSink wires a result sink (the closed-run path)
// and confirms the swallowed 401 never reaches it: only the successful retry does.
func TestRecovered401NotObservedThroughSink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fresh" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	g, tmpls := oneNodeAuth()
	var mu sync.Mutex
	var sunk []StepResult
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls,
		WithResultSink(func(sr StepResult) { mu.Lock(); sunk = append(sunk, sr); mu.Unlock() }),
	)
	holder := NewCredentialHolder(domain.Credential{Secret: "stale"})
	u := VirtualUser{
		ID:      "u0",
		Holder:  holder,
		Refresh: func(context.Context) error { holder.Set(domain.Credential{Secret: "fresh"}); return nil },
	}
	if _, err := r.Run(context.Background(), g, "a", 5, []VirtualUser{u}, 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sunk) != 1 {
		t.Fatalf("sink saw %d results, want 1 (the retry; the 401 is swallowed)", len(sunk))
	}
	if sunk[0].Resp.StatusCode != http.StatusOK {
		t.Errorf("sink result status = %d, want 200", sunk[0].Resp.StatusCode)
	}
}
