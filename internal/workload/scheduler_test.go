package workload

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/obs"
)

// oneNodeGraph is a single-request journey: one session => one request, so the
// launched-session count equals the request count.
func oneNodeGraph() (domain.ScenarioGraph, map[domain.ID]domain.APITemplate) {
	g := domain.ScenarioGraph{
		ID:    "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}},
	}
	tmpls := map[domain.ID]domain.APITemplate{
		"ta": {Method: "GET", Path: "/a"},
	}
	return g, tmpls
}

// twoNodeGraph is a two-request linear journey (a -> b), so think time applies
// once (between the two steps).
func twoNodeGraph() (domain.ScenarioGraph, map[domain.ID]domain.APITemplate) {
	g := domain.ScenarioGraph{
		ID:    "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "tb"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1.0}},
	}
	tmpls := map[domain.ID]domain.APITemplate{
		"ta": {Method: "GET", Path: "/a"},
		"tb": {Method: "GET", Path: "/b"},
	}
	return g, tmpls
}

func newScheduler(t *testing.T, baseURL string, tmpls map[domain.ID]domain.APITemplate, opts ...Option) *Scheduler {
	t.Helper()
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), baseURL, tmpls)
	return New(runner, opts...)
}

// TestConstantArrivalRateLaunchCount checks that a constant-rate run launches
// approximately rate*duration sessions. A virtual clock advances the arrival
// window instantly, so the whole run's worth of arrivals is sampled without
// waiting in real time; the sessions still execute against a real httptest SUT.
func TestConstantArrivalRateLaunchCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := oneNodeGraph()
	clock := newVirtualClock(time.Unix(0, 0))
	s := newScheduler(t, srv.URL, tmpls, WithClock(clock))

	const rate = 200.0
	const secs = 4
	opts := Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 2,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: rate},
			DurationSeconds: secs,
		},
		Seed:  1,
		RunID: "run-const",
	}

	res, err := s.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := rate * secs // 800
	// Poisson variance over a single run: std = sqrt(want) ~= 28. Allow a wide
	// margin so the test is not flaky while still catching gross rate errors.
	lo, hi := int(want*0.80), int(want*1.20)
	if res.Launched < lo || res.Launched > hi {
		t.Errorf("launched = %d, want within [%d,%d] (~%v)", res.Launched, lo, hi, want)
	}
	// Every launched single-request session should have produced exactly one
	// recorded request (no drops, uncapped).
	if res.Dropped != 0 {
		t.Errorf("dropped = %d, want 0 (uncapped)", res.Dropped)
	}
	if res.Stats.Total != res.Launched {
		t.Errorf("requests = %d, launched = %d, want equal (one request per session)", res.Stats.Total, res.Launched)
	}
}

// TestRampLaunchesMoreLaterThanEarlier verifies the scheduler tracks a rising
// rate: under a 0->peak ramp, more arrivals land in the second half of the run
// than the first. A real clock is used so request wall-times map onto run time;
// the handler timestamps each hit relative to the run start.
func TestRampLaunchesMoreLaterThanEarlier(t *testing.T) {
	var start atomic.Int64 // run start, UnixNano; set once
	var firstHalf, secondHalf atomic.Int64
	const dur = 1200 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := time.Now().UnixNano()
		s0 := start.Load()
		if s0 != 0 {
			if time.Duration(now-s0) < dur/2 {
				firstHalf.Add(1)
			} else {
				secondHalf.Add(1)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := oneNodeGraph()
	s := newScheduler(t, srv.URL, tmpls)
	start.Store(time.Now().UnixNano())

	opts := Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 2,
		Model: domain.WorkloadModel{
			Kind: domain.WorkloadOpen,
			Arrival: domain.ArrivalProfile{
				Shape: domain.RateRamp, StartRate: 0, PeakRate: 400, RampSeconds: 1,
			},
			DurationSeconds: 1, // ~1s arrival window (dur gives sessions time to land)
		},
		Seed:  7,
		RunID: "run-ramp",
	}

	res, err := s.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	fh, sh := firstHalf.Load(), secondHalf.Load()
	t.Logf("launched=%d firstHalf=%d secondHalf=%d", res.Launched, fh, sh)
	if sh <= fh {
		t.Errorf("ramp should launch more later: firstHalf=%d secondHalf=%d", fh, sh)
	}
	if res.Launched == 0 {
		t.Fatal("ramp launched nothing")
	}
}

// TestBackPressureCapsConcurrency drives a slow SUT with a high arrival rate and
// a low MaxConcurrency. Live sessions must never exceed the cap, and the excess
// arrivals must be recorded as drops.
func TestBackPressureCapsConcurrency(t *testing.T) {
	const cap = 5
	var live, maxLive int64
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt64(&live, 1)
		mu.Lock()
		if n > maxLive {
			maxLive = n
		}
		mu.Unlock()
		time.Sleep(40 * time.Millisecond) // slow handler: sessions stay live a while
		atomic.AddInt64(&live, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := oneNodeGraph()
	s := newScheduler(t, srv.URL, tmpls)

	opts := Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 2,
		Model: domain.WorkloadModel{
			Kind: domain.WorkloadOpen,
			Arrival: domain.ArrivalProfile{
				Shape: domain.RateConstant, PeakRate: 500, // far exceeds cap/throughput
			},
			DurationSeconds: 1,
			MaxConcurrency:  cap,
		},
		Seed:  3,
		RunID: "run-bp",
	}

	res, err := s.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	peak := maxLive
	mu.Unlock()
	if peak > cap {
		t.Errorf("max live sessions = %d, exceeds cap %d", peak, cap)
	}
	if res.Dropped == 0 {
		t.Errorf("expected drops under back-pressure, got 0 (launched=%d)", res.Launched)
	}
	t.Logf("launched=%d dropped=%d maxLive=%d", res.Launched, res.Dropped, peak)
}

// TestThinkTimeDelaysSession verifies a session with think time takes at least
// the configured minimum. The journey has two steps, so one think pause applies.
func TestThinkTimeDelaysSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := twoNodeGraph()
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	const minMs = 60
	think := thinkFunc(domain.ThinkTime{MinMs: minMs, MaxMs: minMs + 20}, 99)
	if think == nil {
		t.Fatal("thinkFunc returned nil for a non-zero range")
	}

	start := time.Now()
	results, err := runner.RunSession(context.Background(), g, "a", 5,
		load.VirtualUser{ID: "u"}, 1, think)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (a then b)", len(results))
	}
	if elapsed < minMs*time.Millisecond {
		t.Errorf("session took %v, want >= %dms (think time)", elapsed, minMs)
	}
}

// TestThinkFuncRange checks the per-session think draw stays within [min,max].
func TestThinkFuncRange(t *testing.T) {
	think := thinkFunc(domain.ThinkTime{MinMs: 10, MaxMs: 30}, 12345)
	for i := 0; i < 200; i++ {
		d := think()
		if d < 10*time.Millisecond || d > 30*time.Millisecond {
			t.Fatalf("think draw %v out of [10ms,30ms]", d)
		}
	}
	if thinkFunc(domain.ThinkTime{}, 1) != nil {
		t.Error("zero think range should yield nil (no pause)")
	}
}

// TestFindingsParityWithClosedPath runs the same failing SUT through both the
// closed path (load.Runner.Run recorded exactly as internal/api executeLocal does)
// and the open scheduler, and asserts they surface the same finding categories.
func TestFindingsParityWithClosedPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 5xx on the happy path
	}))
	defer srv.Close()

	g, tmpls := twoNodeGraph()
	cfg := obs.ClassifyConfig{ErrorRateThreshold: 0.2, AvailabilityRun: 5}

	// Closed reference: a fixed pool, recorded the way the production closed path
	// records (collector + aggregator keyed by node id, same error class).
	closedCats := closedFindingCategories(t, srv.URL, g, tmpls, cfg)

	// Open path: the scheduler against the identical SUT.
	s := newScheduler(t, srv.URL, tmpls)
	res, err := s.Run(context.Background(), Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 5,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 300},
			DurationSeconds: 1,
		},
		Seed:     5,
		RunID:    "run-open",
		Classify: cfg,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	openCats := categorySet(res.Findings)

	if len(openCats) == 0 {
		t.Fatal("open run produced no findings against a failing SUT")
	}
	if !equalCategorySets(closedCats, openCats) {
		t.Errorf("finding categories differ: closed=%v open=%v", closedCats, openCats)
	}
	// A 5xx on the non-mutated happy path is a contract violation either way.
	if !openCats[domain.FindingContract] {
		t.Errorf("expected a contract finding for 5xx happy path, got %v", openCats)
	}
}

// TestCancellationStopsPromptly cancels a long run shortly after it starts and
// checks Run returns well before the nominal duration.
func TestCancellationStopsPromptly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := oneNodeGraph()
	s := newScheduler(t, srv.URL, tmpls)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := s.Run(ctx, Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 2,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 50},
			DurationSeconds: 60, // would run a full minute if not cancelled
		},
		Seed:  1,
		RunID: "run-cancel",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation not prompt: Run took %v for a 60s nominal run", elapsed)
	}
}

func TestRunRejectsClosedModel(t *testing.T) {
	g, tmpls := oneNodeGraph()
	s := newScheduler(t, "http://x", tmpls)
	_, err := s.Run(context.Background(), Options{
		Graph: g, Start: "a",
		Model: domain.WorkloadModel{Kind: domain.WorkloadClosed, Concurrency: 1},
	})
	if err == nil {
		t.Error("expected error when given a closed model")
	}
}

// TestSetupErrorsFailRunLoudly verifies that a misconfigured graph — a node
// referencing an unknown API template — fails every session's setup, and that
// Run surfaces it as an error with SetupErrors counted rather than reporting a
// healthy, empty run (the failure mode the open path otherwise hides).
func TestSetupErrorsFailRunLoudly(t *testing.T) {
	// Node "a" references template "ta", but the runner is built with no
	// templates, so resolveNodeTemplates fails identically for every session.
	g := domain.ScenarioGraph{
		ID:    "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}},
	}
	clock := newVirtualClock(time.Unix(0, 0))
	s := newScheduler(t, "http://x", map[domain.ID]domain.APITemplate{}, WithClock(clock))

	res, err := s.Run(context.Background(), Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 2,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 50},
			DurationSeconds: 1,
		},
		Seed:  1,
		RunID: "run-misconfig",
	})

	if err == nil {
		t.Fatal("expected an error when every launched session fails to start")
	}
	if res.Launched == 0 {
		t.Error("Launched = 0, want > 0 (sessions are admitted before failing setup)")
	}
	if res.SetupErrors == 0 {
		t.Errorf("SetupErrors = 0, want > 0 (every session failed setup)")
	}
	if res.Stats.Total != 0 {
		t.Errorf("Stats.Total = %d, want 0 (no session produced a request)", res.Stats.Total)
	}
}

// TestProvidedCollectorIsTheLiveSink verifies that a caller-supplied collector
// receives every request (so the control plane can snapshot live progress while
// the run is still in flight) and that its final tally matches the Result — i.e.
// the open path records into the provided sink rather than a private one.
func TestProvidedCollectorIsTheLiveSink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := oneNodeGraph()
	clock := newVirtualClock(time.Unix(0, 0))
	s := newScheduler(t, srv.URL, tmpls, WithClock(clock))

	sink := obs.NewCollector()
	res, err := s.Run(context.Background(), Options{
		Graph:    g,
		Start:    "a",
		MaxSteps: 2,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 100},
			DurationSeconds: 2,
		},
		Seed:      1,
		RunID:     "run-sink",
		Collector: sink,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.Total == 0 {
		t.Fatal("expected some requests")
	}
	if got := sink.Snapshot().Total; got != res.Stats.Total {
		t.Errorf("provided collector total = %d, result total = %d, want equal", got, res.Stats.Total)
	}
}

// TestSegmentsSplitTrafficByWeight verifies the persona mix: two segments with a
// 3:1 weight and distinct entry nodes should send roughly 75% / 25% of arrivals
// down their respective paths. Each segment's start node maps to its own
// endpoint, so counting hits per path measures the realized split.
func TestSegmentsSplitTrafficByWeight(t *testing.T) {
	var aHits, bHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			aHits.Add(1)
		case "/b":
			bHits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Two disconnected single-request nodes; a session enters at its segment's
	// start node and issues exactly one request there.
	g := domain.ScenarioGraph{
		ID: "g",
		Nodes: []domain.Node{
			{ID: "a", APITemplateID: "ta"},
			{ID: "b", APITemplateID: "tb"},
		},
	}
	tmpls := map[domain.ID]domain.APITemplate{
		"ta": {Method: "GET", Path: "/a"},
		"tb": {Method: "GET", Path: "/b"},
	}
	clock := newVirtualClock(time.Unix(0, 0))
	s := newScheduler(t, srv.URL, tmpls, WithClock(clock))

	res, err := s.Run(context.Background(), Options{
		Graph:    g,
		Start:    "a", // run default; each segment overrides it
		MaxSteps: 2,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 400},
			DurationSeconds: 4,
		},
		Seed:  7,
		RunID: "run-segments",
		Segments: []domain.Segment{
			{Name: "heavy", Weight: 3, Start: "a"},
			{Name: "light", Weight: 1, Start: "b"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	a, b := aHits.Load(), bHits.Load()
	total := a + b
	if total == 0 || int(total) != res.Stats.Total {
		t.Fatalf("hits a=%d b=%d (sum %d) != stats total %d", a, b, total, res.Stats.Total)
	}
	// Expect ~75% to the heavy segment; allow a wide margin for sampling variance.
	shareA := float64(a) / float64(total)
	if shareA < 0.65 || shareA > 0.85 {
		t.Errorf("heavy-segment share = %.2f, want ~0.75 (a=%d b=%d)", shareA, a, b)
	}
}

// TestRunRejectsInvalidSegments verifies the scheduler validates the persona mix
// up front rather than launching a broken run.
func TestRunRejectsInvalidSegments(t *testing.T) {
	g, tmpls := oneNodeGraph()
	s := newScheduler(t, "http://x", tmpls)
	_, err := s.Run(context.Background(), Options{
		Graph: g, Start: "a", MaxSteps: 1,
		Model: domain.WorkloadModel{
			Kind:            domain.WorkloadOpen,
			Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 10},
			DurationSeconds: 1,
		},
		Segments: []domain.Segment{{Name: "a", Weight: 1}, {Name: "a", Weight: 1}}, // dup name
	})
	if err == nil {
		t.Error("expected error for an invalid segment mix")
	}
}

// --- helpers ---

// closedFindingCategories runs a fixed pool of users through the closed runner
// and classifies findings using the SAME recording the control-plane closed path
// uses (internal/api Server.executeLocal): collector.Record + an aggregator
// observation keyed by the node id with the matching error class.
func closedFindingCategories(t *testing.T, baseURL string, g domain.ScenarioGraph, tmpls map[domain.ID]domain.APITemplate, cfg obs.ClassifyConfig) map[domain.FindingCategory]bool {
	t.Helper()
	runner := load.NewRunner(load.NewRESTAdapter(2*time.Second), baseURL, tmpls)
	users := make([]load.VirtualUser, 30)
	for i := range users {
		users[i] = load.VirtualUser{ID: "vu"}
	}
	results, err := runner.Run(context.Background(), g, "a", 5, users, 5)
	if err != nil {
		t.Fatalf("closed run: %v", err)
	}
	agg := obs.NewAggregator()
	ts := time.Now()
	for _, sr := range results {
		agg.Add(obs.RequestObservation{
			APIID:      sr.NodeID,
			StatusCode: sr.Resp.StatusCode,
			LatencyMs:  sr.Resp.LatencyMs,
			ErrorClass: errorClass(sr),
			TS:         ts,
		})
	}
	return categorySet(agg.Classify("run-closed", cfg))
}

func categorySet(findings []domain.Finding) map[domain.FindingCategory]bool {
	out := map[domain.FindingCategory]bool{}
	for _, f := range findings {
		out[f.Category] = true
	}
	return out
}

func equalCategorySets(a, b map[domain.FindingCategory]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
