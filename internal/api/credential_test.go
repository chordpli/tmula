package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
)

// authEchoSUT is a system under test that records the Authorization header of
// every request, so a test can assert which credential each virtual user sent.
// It always answers 200 so the run completes cleanly.
type authEchoSUT struct {
	mu    sync.Mutex
	auths []string
}

func newAuthEchoSUT() (*httptest.Server, *authEchoSUT) {
	rec := &authEchoSUT{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.auths = append(rec.auths, r.Header.Get("Authorization"))
		rec.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return srv, rec
}

// distinct returns the sorted set of Authorization headers seen.
func (a *authEchoSUT) distinct() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := map[string]struct{}{}
	for _, h := range a.auths {
		set[h] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// specAuth builds a single-node closed spec whose one template echoes the bearer
// token, so the SUT records exactly one Authorization header per virtual-user
// visit. UserCount sizes the pool from a count (no explicit user array), so the
// per-user credential is assigned by the server, not shipped in the request.
func specAuth(sutURL string, userCount int, pool *domain.CredentialPool) RunSpec {
	return RunSpec{
		Experiment: domain.Experiment{
			Name: "auth", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: userCount, DeviationRate: 0, AuthStrategy: domain.CredPool},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL: sutURL, Allowlist: []string{"127.0.0.1"},
			RateCap: domain.RateCap{MaxRPS: 10000, MaxConcurrency: 1000}, EnvClass: domain.EnvDev,
		},
		Graph: domain.ScenarioGraph{
			ID:    "g",
			Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}},
		},
		Templates: map[domain.ID]domain.APITemplate{
			"ta": {Method: "GET", Path: "/a", Headers: map[string]string{"Authorization": "Bearer {{.token}}"}},
		},
		Start: "a", MaxSteps: 1, UserCount: userCount, Seed: 1,
		CredentialPool: pool,
	}
}

// twoEntryPool is a pre-supplied pool with two distinct credentials.
func twoEntryPool() *domain.CredentialPool {
	return &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredPool,
		Entries: []domain.Credential{
			{Subject: "u0", Secret: "tok-0"},
			{Subject: "u1", Secret: "tok-1"},
		},
	}
}

// runInProcess drives a spec through the control plane's Go API (create → start →
// poll) without a JSON round-trip, so a credential secret (json:"-") survives to
// the runtime — the path the in-process `tmula run` CLI uses. It returns the
// terminal report.
func runInProcess(t *testing.T, spec RunSpec, timeout time.Duration) Report {
	t.Helper()
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	id, err := srv.CreateExperiment(spec)
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}
	runID, err := srv.StartRun(id)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		rep, ok := srv.Report(runID)
		if !ok {
			t.Fatalf("run %s not found", runID)
		}
		switch rep.Run.Status {
		case domain.RunCompleted, domain.RunFailed, domain.RunKilled:
			return rep
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish within %s (last status %q)", timeout, rep.Run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestClosedRunInjectsPoolCredentialsPerUser drives a closed run of three users
// against a two-entry pool and asserts user 0 and user 1 send distinct tokens and
// user 2 wraps back to entry 0 — the pool provider keyed by user index, end to end.
func TestClosedRunInjectsPoolCredentialsPerUser(t *testing.T) {
	sut, rec := newAuthEchoSUT()
	defer sut.Close()

	rep := runInProcess(t, specAuth(sut.URL, 3, twoEntryPool()), 3*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	if rep.Stats.Total != 3 { // 3 users * 1 node
		t.Fatalf("stats.Total = %d, want 3", rep.Stats.Total)
	}

	// Three users, two pool entries: user 0 -> tok-0, user 1 -> tok-1, user 2 -> tok-0.
	got := rec.distinct()
	want := []string{"Bearer tok-0", "Bearer tok-1"}
	if len(got) != len(want) {
		t.Fatalf("distinct auth headers = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("auth header[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestClosedRunNoPoolIsUnauthenticated proves a run with no credential pool sends
// an empty bearer token for every user, i.e. nothing changed for the existing
// (anonymous) path.
func TestClosedRunNoPoolIsUnauthenticated(t *testing.T) {
	sut, rec := newAuthEchoSUT()
	defer sut.Close()

	rep := runInProcess(t, specAuth(sut.URL, 3, nil), 3*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}

	// No pool: {{.token}} renders empty, so the header is "Bearer " — and Go's HTTP
	// server trims the trailing space, so the SUT sees exactly "Bearer" with no
	// token. There is no per-user credential.
	got := rec.distinct()
	if len(got) != 1 || got[0] != "Bearer" {
		t.Fatalf("distinct auth headers = %q, want [\"Bearer\"] (unauthenticated)", got)
	}
}

// TestOpenRunInjectsPoolCredentialsPerSession runs an open (arrival-rate) workload
// against a two-entry pool and asserts both pool credentials show up across the
// sessions — the scheduler assigned a credential per arrival by its global index.
func TestOpenRunInjectsPoolCredentialsPerSession(t *testing.T) {
	sut, rec := newAuthEchoSUT()
	defer sut.Close()

	spec := specAuth(sut.URL, 0, twoEntryPool())
	// The open model generates its own sessions from the arrival rate; a single
	// base identity suffices, but the experiment still needs a positive user count.
	spec.Experiment.Params.VirtualUserCount = 1
	// Open model: a steady arrival rate over a short window generates many
	// sessions, each authenticating from the pool by its arrival index.
	spec.Workload = &domain.WorkloadModel{
		Kind:            domain.WorkloadOpen,
		Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, StartRate: 200, PeakRate: 200},
		DurationSeconds: 1,
	}

	rep := runInProcess(t, spec, 5*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	if rep.Stats.Total < 2 {
		t.Fatalf("open run produced %d requests, want >= 2 to exercise both pool entries", rep.Stats.Total)
	}

	// Across the sessions, both distinct pool credentials must appear (arrivals are
	// keyed 0,1,2,... so the first two arrivals alone cover both entries).
	got := rec.distinct()
	want := map[string]bool{"Bearer tok-0": false, "Bearer tok-1": false}
	for _, h := range got {
		if _, ok := want[h]; ok {
			want[h] = true
		}
	}
	for h, seen := range want {
		if !seen {
			t.Errorf("open run never sent credential %q (got %q)", h, got)
		}
	}
}

// TestValidateRejectsBadCredentialPool covers the spec-level guards: an empty
// "pool" pool, an unknown strategy, the not-yet-supported bootstrap-signup
// strategy, and a credential pool combined with distributed workers are all
// rejected, while a valid pool passes.
func TestValidateRejectsBadCredentialPool(t *testing.T) {
	base := func() RunSpec { return specFor("http://127.0.0.1:1", 1) }

	// Valid pool: accepted.
	ok := base()
	ok.CredentialPool = twoEntryPool()
	if err := ok.Validate(); err != nil {
		t.Errorf("valid pool rejected: %v", err)
	}

	// Empty "pool" strategy: rejected (no entries to hand out).
	empty := base()
	empty.CredentialPool = &domain.CredentialPool{ID: "p", Strategy: domain.CredPool}
	if err := empty.Validate(); err == nil {
		t.Error("empty pool should be rejected")
	}

	// Unknown strategy: rejected.
	unknown := base()
	unknown.CredentialPool = &domain.CredentialPool{ID: "p", Strategy: domain.CredentialStrategy("oauth2")}
	if err := unknown.Validate(); err == nil {
		t.Error("unknown credential strategy should be rejected")
	}

	// Bootstrap-signup: rejected with a clear not-yet-supported message.
	flow := domain.ID("signup")
	boot := base()
	boot.CredentialPool = &domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, BootstrapFlowID: &flow}
	err := boot.Validate()
	if err == nil {
		t.Fatal("bootstrap-signup should be rejected on this path")
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("bootstrap-signup error = %q, want a not-yet-supported message", err)
	}

	// Credential pool + distributed workers: rejected (workers synthesize their own users).
	dist := base()
	dist.CredentialPool = twoEntryPool()
	dist.Workers = []string{"127.0.0.1:65535"}
	if err := dist.Validate(); err == nil {
		t.Error("credential pool with workers should be rejected")
	}
}

// TestCreateExperimentRejectsBootstrapPool confirms the Go-level submission path
// (used by the in-process CLI) enforces the same guard as Validate: a
// bootstrap-signup pool is rejected rather than silently run unauthenticated.
func TestCreateExperimentRejectsBootstrapPool(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(time.Second))
	flow := domain.ID("signup")
	spec := specAuth("http://127.0.0.1:1", 1, &domain.CredentialPool{
		ID: "p", Strategy: domain.CredBootstrapSignup, BootstrapFlowID: &flow,
	})
	if _, err := srv.CreateExperiment(spec); err == nil {
		t.Fatal("CreateExperiment should reject a bootstrap-signup pool")
	}
}

// TestSpecMarshalNeverLeaksSecret confirms a persisted/streamed RunSpec carries the
// non-sensitive subject but never the credential secret — the json:"-" guarantee
// holds through the spec, not just the bare domain.Credential. This is what makes
// the secret unable to cross the wire (and why the in-process path exists).
func TestSpecMarshalNeverLeaksSecret(t *testing.T) {
	spec := specAuth("http://127.0.0.1:1", 0, twoEntryPool())
	// Also pin a secret on an explicit user to prove that path is masked too.
	spec.Users = []load.VirtualUser{{ID: "u", Cred: domain.Credential{Subject: "alice", Secret: "super-secret"}}}

	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, secret := range []string{"tok-0", "tok-1", "super-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("marshalled spec leaked secret %q: %s", secret, out)
		}
	}
	// The non-sensitive subject is fine to persist and must survive.
	if !strings.Contains(out, "u0") || !strings.Contains(out, "alice") {
		t.Errorf("marshalled spec dropped the non-sensitive subject: %s", out)
	}
}
