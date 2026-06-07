package workload

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/auth"
	"github.com/chordpli/tmula/internal/domain"
)

// TestSchedulerInjectsPerSessionCredentials drives an open run with an auth
// provider and a two-entry pool against an Authorization-echoing SUT, then asserts
// every session authenticated and both distinct pool credentials were sent — the
// scheduler keys each arrival's credential by its global index. A virtual clock
// advances the whole arrival window instantly, so the run is deterministic.
func TestSchedulerInjectsPerSessionCredentials(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("Authorization")]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := oneNodeGraph()
	// Echo the bearer token so the SUT records each session's credential.
	tmpls["ta"] = domain.APITemplate{
		Method: "GET", Path: "/a",
		Headers: map[string]string{"Authorization": "Bearer {{.token}}"},
	}

	provider, err := auth.NewProvider(domain.CredentialPool{
		Strategy: domain.CredPool,
		Entries: []domain.Credential{
			{Subject: "u0", Secret: "tok-0"},
			{Subject: "u1", Secret: "tok-1"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	clock := newVirtualClock(time.Unix(0, 0))
	s := newScheduler(t, srv.URL, tmpls, WithClock(clock))
	res, err := s.Run(context.Background(), Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 1,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 50},
			DurationSeconds: 4,
		},
		Seed:  1,
		RunID: "run-auth",
		Auth:  provider,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Launched < 2 {
		t.Fatalf("launched %d sessions, want >= 2 to exercise both pool entries", res.Launched)
	}

	mu.Lock()
	defer mu.Unlock()
	// No session may be unauthenticated, and both pool credentials must appear.
	if n := seen["Bearer"] + seen["Bearer "]; n != 0 {
		t.Errorf("%d sessions sent no token; every session must authenticate", n)
	}
	if seen["Bearer tok-0"] == 0 || seen["Bearer tok-1"] == 0 {
		t.Errorf("auth headers seen = %v, want both tok-0 and tok-1", seen)
	}
}

// TestSchedulerNoAuthIsUnauthenticated confirms that without an Auth provider the
// scheduler leaves every session unauthenticated — the credential-injection path
// is opt-in and changes nothing when no pool is set.
func TestSchedulerNoAuthIsUnauthenticated(t *testing.T) {
	var mu sync.Mutex
	tokens := map[string]struct{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokens[r.Header.Get("Authorization")] = struct{}{}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := oneNodeGraph()
	tmpls["ta"] = domain.APITemplate{
		Method: "GET", Path: "/a",
		Headers: map[string]string{"Authorization": "Bearer {{.token}}"},
	}

	clock := newVirtualClock(time.Unix(0, 0))
	s := newScheduler(t, srv.URL, tmpls, WithClock(clock))
	res, err := s.Run(context.Background(), Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 1,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 50},
			DurationSeconds: 2,
		},
		Seed:  1,
		RunID: "run-noauth",
		// Auth deliberately nil.
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Launched == 0 {
		t.Fatal("no sessions launched")
	}
	// Every request carries an empty bearer (the server trims the trailing space),
	// so the only token value ever seen is "Bearer".
	mu.Lock()
	defer mu.Unlock()
	for tok := range tokens {
		if tok != "Bearer" {
			t.Errorf("unexpected token %q with no auth provider, want only \"Bearer\"", tok)
		}
	}
}
