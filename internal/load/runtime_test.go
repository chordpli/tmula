package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

func linearGraph() (domain.ScenarioGraph, map[domain.ID]domain.APITemplate) {
	g := domain.ScenarioGraph{
		ID:    "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "tb"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1.0}},
	}
	tmpls := map[domain.ID]domain.APITemplate{
		"ta": {Method: "GET", Path: "/a", Headers: map[string]string{"X-Tok": "{{.token}}"}},
		"tb": {Method: "GET", Path: "/b", Headers: map[string]string{"X-Tok": "{{.token}}"}},
	}
	return g, tmpls
}

func TestRunConcurrentUsers(t *testing.T) {
	var hits int64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	const users = 50
	vus := make([]VirtualUser, users)
	for i := range vus {
		vus[i] = VirtualUser{ID: "u" + string(rune('A'+i%26))}
	}

	results, err := r.Run(context.Background(), g, "a", 5, vus, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Each user visits a and b => 2 sends per user.
	if hits != users*2 {
		t.Errorf("server hits = %d, want %d", hits, users*2)
	}
	if len(results) != users*2 {
		t.Errorf("results = %d, want %d", len(results), users*2)
	}
	for _, sr := range results {
		if sr.Err != nil || sr.Resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected step result: %+v", sr)
		}
	}
}

func TestRunCancelledContextSendsNothing(t *testing.T) {
	var hits int64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before run

	vus := []VirtualUser{{ID: "u1"}, {ID: "u2"}}
	if _, err := r.Run(ctx, g, "a", 5, vus, 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	if hits != 0 {
		t.Errorf("cancelled run should send nothing, got %d hits", hits)
	}
}

func TestRunIndependentCredentials(t *testing.T) {
	seen := map[string]bool{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen[req.Header.Get("X-Tok")] = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	vus := []VirtualUser{
		{ID: "u1", Cred: domain.Credential{Secret: "tok-1"}},
		{ID: "u2", Cred: domain.Credential{Secret: "tok-2"}},
		{ID: "u3", Cred: domain.Credential{Secret: "tok-3"}},
	}
	if _, err := r.Run(context.Background(), g, "a", 5, vus, 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, tok := range []string{"tok-1", "tok-2", "tok-3"} {
		if !seen[tok] {
			t.Errorf("server never saw credential %q", tok)
		}
	}
}

func TestRunUnknownTemplateErrors(t *testing.T) {
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "a", APITemplateID: "missing"}},
	}
	r := NewRunner(NewRESTAdapter(time.Second), "http://x", map[domain.ID]domain.APITemplate{})
	if _, err := r.Run(context.Background(), g, "a", 1, []VirtualUser{{ID: "u"}}, 1); err == nil {
		t.Fatal("expected error for node referencing unknown template")
	}
}
