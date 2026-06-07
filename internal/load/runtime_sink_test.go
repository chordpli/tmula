package load

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/obs"
)

// fakeAdapter is a deterministic in-memory SUT: it returns a fixed status and
// latency per template path, so a run's stats and findings are reproducible
// regardless of timing. An optional onSend hook lets a test observe concurrency.
type fakeAdapter struct {
	// byPath maps a rendered request path to the response the SUT returns for it.
	byPath map[string]Response
	// onSend, when set, runs at the start of every Send (before the response is
	// produced); tests use it to count in-flight requests.
	onSend func()
}

func (f *fakeAdapter) Protocol() domain.Protocol { return domain.ProtocolREST }

func (f *fakeAdapter) Send(_ context.Context, req RenderedRequest) (Response, error) {
	if f.onSend != nil {
		f.onSend()
	}
	// Match on the path suffix so the baseURL prefix does not matter.
	for path, resp := range f.byPath {
		if len(req.URL) >= len(path) && req.URL[len(req.URL)-len(path):] == path {
			return resp, nil
		}
	}
	return Response{StatusCode: 200, LatencyMs: 1}, nil
}

// foldStats drives a closed run through the given runner-construction options and
// folds every step (whether it arrived via the returned slice or a result sink)
// into a fresh Collector + Aggregator, returning the run-wide stats and findings.
// It is the shared body the golden-compare test uses for both paths.
func foldStats(t *testing.T, adapter Adapter, g domain.ScenarioGraph, tmpls map[domain.ID]domain.APITemplate, users []VirtualUser, useSink bool) (obs.Stats, []domain.Finding) {
	t.Helper()
	collector := obs.NewCollector()
	agg := obs.NewAggregator()
	ts := time.Unix(0, 0) // fixed so first-seen ordering is identical across paths

	fold := func(sr StepResult) {
		cls := ""
		if sr.Err != nil {
			cls = "transport"
		}
		collector.Record(sr.Resp.StatusCode, sr.Resp.LatencyMs, cls)
		agg.Add(obs.RequestObservation{
			APIID:      sr.NodeID,
			StatusCode: sr.Resp.StatusCode,
			LatencyMs:  sr.Resp.LatencyMs,
			ErrorClass: cls,
			TS:         ts,
		})
	}

	opts := []RunnerOption{}
	if useSink {
		opts = append(opts, WithResultSink(fold))
	}
	r := NewRunner(adapter, "http://sut.local", tmpls, opts...)
	results, err := r.Run(context.Background(), g, "a", 5, users, 7)
	if err != nil {
		t.Fatalf("run (sink=%v): %v", useSink, err)
	}
	if useSink {
		if len(results) != 0 {
			t.Fatalf("sink run returned %d results, want 0 (sink owns them)", len(results))
		}
	} else {
		for _, sr := range results {
			fold(sr)
		}
	}
	cfg := obs.ClassifyConfig{ErrorRateThreshold: 0.2, AvailabilityRun: 5}
	return collector.Snapshot(), agg.Classify("run-1", cfg)
}

// TestRunResultSinkMatchesSlice is the golden compare: a run folded from the
// returned slice and the same run folded through a ResultSink must yield byte-for
// byte identical stats and findings, so the streaming path is a drop-in for the
// buffering one. The scenario mixes a healthy endpoint and a 500 endpoint so both
// the threshold and contract classifiers fire.
func TestRunResultSinkMatchesSlice(t *testing.T) {
	t.Parallel()

	// a -> b: a is healthy (200), b returns 500 on the happy path (a contract
	// violation, and enough errors to trip the error-rate threshold).
	g := domain.ScenarioGraph{
		ID:    "g",
		Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "tb"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1.0}},
	}
	tmpls := map[domain.ID]domain.APITemplate{
		"ta": {Method: "GET", Path: "/a"},
		"tb": {Method: "GET", Path: "/b"},
	}
	adapter := &fakeAdapter{byPath: map[string]Response{
		"/a": {StatusCode: 200, LatencyMs: 12},
		"/b": {StatusCode: 500, LatencyMs: 34},
	}}

	users := make([]VirtualUser, 200)
	for i := range users {
		users[i] = VirtualUser{ID: fmt.Sprintf("u%d", i)}
	}

	sliceStats, sliceFindings := foldStats(t, adapter, g, tmpls, users, false)
	sinkStats, sinkFindings := foldStats(t, adapter, g, tmpls, users, true)

	if !statsEqual(sliceStats, sinkStats) {
		t.Fatalf("stats differ:\n slice = %+v\n sink  = %+v", sliceStats, sinkStats)
	}
	if !findingsEqual(sliceFindings, sinkFindings) {
		t.Fatalf("findings differ:\n slice = %+v\n sink  = %+v", sliceFindings, sinkFindings)
	}
	// Sanity: the scenario actually exercised the classifiers (otherwise an empty
	// equality is a hollow pass).
	if len(sinkFindings) == 0 {
		t.Fatal("expected findings from the 500 endpoint; the golden compare proved nothing")
	}
	if sinkStats.Total != len(users)*2 {
		t.Fatalf("total = %d, want %d (2 requests per user)", sinkStats.Total, len(users)*2)
	}
}

// statsEqual compares two obs.Stats including their StatusCounts maps (obs.Stats
// holds a map, so it is not directly comparable with ==).
func statsEqual(a, b obs.Stats) bool {
	if a.Total != b.Total || a.Errors != b.Errors || a.Timeouts != b.Timeouts ||
		a.ErrorRate != b.ErrorRate || a.P50 != b.P50 || a.P95 != b.P95 ||
		a.P99 != b.P99 || a.Max != b.Max {
		return false
	}
	if len(a.StatusCounts) != len(b.StatusCounts) {
		return false
	}
	for code, n := range a.StatusCounts {
		if b.StatusCounts[code] != n {
			return false
		}
	}
	return true
}

// findingsEqual reports whether two finding slices match on every classifier-
// relevant field, order-independent (the Aggregator groups per API per category,
// so the slice order can vary between runs). The per-API identity lives in the
// Description and FirstSeen, both of which the fixed timestamp keeps stable across
// the two paths.
func findingsEqual(a, b []domain.Finding) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(f domain.Finding) string {
		return fmt.Sprintf("%s|%s|%s|%s|%d", f.RunID, f.Category, f.Severity, f.Description, f.FirstSeen.UnixNano())
	}
	counts := make(map[string]int, len(a))
	for _, f := range a {
		counts[key(f)]++
	}
	for _, f := range b {
		counts[key(f)]--
	}
	for _, n := range counts {
		if n != 0 {
			return false
		}
	}
	return true
}

// TestRunResultSinkConcurrencySafe drives many users through a sink that only
// touches an atomic, so -race proves the Runner fans results out to a shared sink
// without a data race and the count proves every request reached it. A plain
// (unsynchronized) sink mutating shared state here would trip the race detector.
func TestRunResultSinkConcurrencySafe(t *testing.T) {
	t.Parallel()

	g, tmpls := linearGraph() // a -> b, two requests per user
	adapter := &fakeAdapter{byPath: map[string]Response{
		"/a": {StatusCode: 200, LatencyMs: 5},
		"/b": {StatusCode: 200, LatencyMs: 5},
	}}

	const users = 500
	vus := make([]VirtualUser, users)
	for i := range vus {
		vus[i] = VirtualUser{ID: fmt.Sprintf("u%d", i)}
	}

	var got int64
	r := NewRunner(adapter, "http://sut.local", tmpls,
		WithResultSink(func(StepResult) { atomic.AddInt64(&got, 1) }))

	results, err := r.Run(context.Background(), g, "a", 5, vus, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("sink run returned %d results, want 0", len(results))
	}
	if want := int64(users * 2); got != want {
		t.Fatalf("sink saw %d results, want %d", got, want)
	}
}

// TestRunBoundsConcurrency proves the fan-out is actually bounded: with a cap far
// below the user count, the number of sessions in flight at once (observed via a
// live counter in the adapter) never exceeds the cap — yet every user still runs
// and is still seeded by seed+i. Without the semaphore the peak would reach the
// user count (one goroutine per user).
func TestRunBoundsConcurrency(t *testing.T) {
	t.Parallel()

	g, tmpls := linearGraph()

	const (
		users = 200
		limit = 8
	)

	var (
		live int64 // sessions currently inside Send
		peak int64 // high-water mark of live
	)
	adapter := &fakeAdapter{
		byPath: map[string]Response{
			"/a": {StatusCode: 200, LatencyMs: 1},
			"/b": {StatusCode: 200, LatencyMs: 1},
		},
		onSend: func() {
			n := atomic.AddInt64(&live, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if n <= old || atomic.CompareAndSwapInt64(&peak, old, n) {
					break
				}
			}
			// Hold the slot briefly so concurrent sessions pile up to the cap; if the
			// bound is broken this is when live would shoot past limit.
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt64(&live, -1)
		},
	}

	vus := make([]VirtualUser, users)
	for i := range vus {
		vus[i] = VirtualUser{ID: fmt.Sprintf("u%d", i)}
	}

	// A seed-witness sink records, per user, the seed the run used: user i must be
	// seeded seed+i regardless of which pool worker ran it. We recover the seed
	// from the deterministic walk rather than trusting the pool order.
	var mu sync.Mutex
	seenUsers := make(map[string]bool, users)
	r := NewRunner(adapter, "http://sut.local", tmpls,
		withMaxConcurrency(limit),
		WithResultSink(func(sr StepResult) {
			mu.Lock()
			seenUsers[sr.UserID] = true
			mu.Unlock()
		}))

	const seed = 99
	if _, err := r.Run(context.Background(), g, "a", 5, vus, seed); err != nil {
		t.Fatalf("run: %v", err)
	}

	if p := atomic.LoadInt64(&peak); p > limit {
		t.Fatalf("peak concurrency = %d, want <= %d (fan-out not bounded)", p, limit)
	} else if p == 0 {
		t.Fatal("peak concurrency = 0; the adapter never observed an in-flight session")
	}
	// Every user must still have run.
	if len(seenUsers) != users {
		t.Fatalf("ran %d distinct users, want %d", len(seenUsers), users)
	}
	for i := 0; i < users; i++ {
		if !seenUsers[fmt.Sprintf("u%d", i)] {
			t.Fatalf("user u%d never ran under the bounded pool", i)
		}
	}

	// Determinism guard: the bounded pool must seed user i with seed+i, identical
	// to the unbounded fan-out. Re-run with the production-default cap and compare
	// the full ordered step stream; any reseeding would diverge the walks.
	bounded := orderedSteps(t, NewRunner(adapter, "http://sut.local", tmpls, withMaxConcurrency(limit)), g, vus, seed)
	unbounded := orderedSteps(t, NewRunner(adapter, "http://sut.local", tmpls), g, vus, seed)
	if len(bounded) != len(unbounded) {
		t.Fatalf("bounded produced %d steps, unbounded %d", len(bounded), len(unbounded))
	}
	for k, v := range unbounded {
		if bounded[k] != v {
			t.Fatalf("step stream diverged for %q: bounded=%d unbounded=%d (seeding changed under the pool)", k, bounded[k], v)
		}
	}
}

// orderedSteps runs a closed pool and returns a per-(user,node) request count.
// Keying by user+node rather than completion order means two runs with the same
// seeds compare equal even though the bounded and unbounded pools may interleave
// the sessions differently; identical keys+counts across the two runs means each
// user walked the same seeded path both times. r already carries its concurrency
// cap; a witnessing sink is layered on via a shallow copy so the cap is preserved.
func orderedSteps(t *testing.T, r *Runner, g domain.ScenarioGraph, users []VirtualUser, seed int64) map[string]int {
	t.Helper()
	var mu sync.Mutex
	out := make(map[string]int)
	witness := *r
	witness.resultSink = func(sr StepResult) {
		mu.Lock()
		out[fmt.Sprintf("%s->%s", sr.UserID, sr.NodeID)]++
		mu.Unlock()
	}
	if _, err := witness.Run(context.Background(), g, "a", 5, users, seed); err != nil {
		t.Fatalf("ordered run: %v", err)
	}
	return out
}
