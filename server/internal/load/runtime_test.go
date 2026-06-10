package load

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
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

func TestRunEmitsTerminalEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// a (a request) -> done (a template-less terminal: no request fires).
	g := domain.ScenarioGraph{
		ID:    "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "done"}},
		Edges: []domain.Edge{{From: "a", To: "done", Weight: 1.0}},
	}
	tmpls := map[domain.ID]domain.APITemplate{"ta": {Method: "GET", Path: "/a"}}

	var mu sync.Mutex
	var events []StepEvent
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls,
		WithEventSink(func(e StepEvent) { mu.Lock(); events = append(events, e); mu.Unlock() }))

	if _, err := r.Run(context.Background(), g, "a", 5, []VirtualUser{{ID: "u"}}, 1); err != nil {
		t.Fatalf("run: %v", err)
	}

	var req, term *StepEvent
	for i := range events {
		switch {
		case events[i].To == "a" && !events[i].Terminal:
			req = &events[i]
		case events[i].To == "done" && events[i].Terminal:
			term = &events[i]
		}
	}
	if req == nil {
		t.Fatal("expected a request event for node a")
	}
	if term == nil {
		t.Fatal("expected a terminal event reaching done (so the funnel can show completions)")
	}
	if term.From != "a" || !term.OK || term.Status != 0 || term.LatencyMs != 0 {
		t.Errorf("terminal event = %+v, want {From:a OK:true Status:0 LatencyMs:0 Terminal:true}", *term)
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

func TestRunAddsCorrelationHeadersPerStep(t *testing.T) {
	seen := map[string]http.Header{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen[req.URL.Path] = req.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls, WithCorrelationIDs("run-1", "scenario-1"))

	if _, err := r.Run(context.Background(), g, "a", 5, []VirtualUser{{ID: "u1"}}, 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	assertHeader(t, seen["/a"], HeaderRunID, "run-1")
	assertHeader(t, seen["/a"], HeaderScenarioID, "scenario-1")
	assertHeader(t, seen["/a"], HeaderSessionID, "u1")
	assertHeader(t, seen["/a"], HeaderNodeID, "a")
	assertHeader(t, seen["/b"], HeaderRunID, "run-1")
	assertHeader(t, seen["/b"], HeaderScenarioID, "scenario-1")
	assertHeader(t, seen["/b"], HeaderSessionID, "u1")
	assertHeader(t, seen["/b"], HeaderNodeID, "b")
}

func TestRunCorrelationDefaultsScenarioIDFromGraph(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := domain.ScenarioGraph{
		ID:    "graph-default",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}},
	}
	tmpls := map[domain.ID]domain.APITemplate{"ta": {Method: "GET", Path: "/a"}}
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls, WithCorrelationIDs("run-1", ""))

	if _, err := r.Run(context.Background(), g, "a", 1, []VirtualUser{{ID: "u1"}}, 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	assertHeader(t, got, HeaderScenarioID, "graph-default")
}

func TestRunExtractsResponseVariablesPerSession(t *testing.T) {
	var mu sync.Mutex
	cartBodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		session := req.Header.Get(HeaderSessionID)
		switch req.URL.Path {
		case "/products":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"items":[{"id":"product-%s"}]}`, session)
		case "/cart":
			body, _ := io.ReadAll(req.Body)
			mu.Lock()
			cartBodies[session] = string(body)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()

	g := domain.ScenarioGraph{
		ID:    "shop",
		Nodes: []domain.Node{{ID: "products", APITemplateID: "t_products"}, {ID: "cart", APITemplateID: "t_cart"}},
		Edges: []domain.Edge{{From: "products", To: "cart", Weight: 1}},
	}
	tmpls := map[domain.ID]domain.APITemplate{
		"t_products": {Method: "GET", Path: "/products", Extract: map[string]string{"productId": "items.0.id"}},
		"t_cart":     {Method: "POST", Path: "/cart", PayloadTemplate: `{"productId":"{{.productId}}"}`},
	}
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls, WithCorrelationIDs("run-1", "shop"))

	users := []VirtualUser{{ID: "u1"}, {ID: "u2"}}
	results, err := r.Run(context.Background(), g, "products", 2, users, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, sr := range results {
		if sr.Err != nil {
			t.Fatalf("unexpected step error: %+v", sr)
		}
	}
	want := map[string]string{
		"u1": `{"productId":"product-u1"}`,
		"u2": `{"productId":"product-u2"}`,
	}
	for session, body := range want {
		if cartBodies[session] != body {
			t.Errorf("cart body for %s = %q, want %q", session, cartBodies[session], body)
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
