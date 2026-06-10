package obs

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

func obsv(status int, latency float64, errClass string, mutated bool) RequestObservation {
	return RequestObservation{StatusCode: status, LatencyMs: latency, ErrorClass: errClass, Mutated: mutated}
}

// TestSummaryStatsMixed checks counters, error rate, timeouts, status counts
// and plausible percentiles for a small mixed dataset.
func TestSummaryStatsMixed(t *testing.T) {
	s := NewSummary()
	// 6 observations mirroring collector_test's TestErrorRateAndTimeouts so the
	// Summary's counting matches the Collector's exactly.
	s.Add(obsv(200, 10, "", false))
	s.Add(obsv(200, 12, "", false))
	s.Add(obsv(500, 30, "", false))              // error (5xx)
	s.Add(obsv(0, 0, TimeoutClass, false))       // error + timeout
	s.Add(obsv(404, 5, "", false))               // error (4xx)
	s.Add(obsv(0, 0, "connection reset", false)) // error (transport)

	st := s.Stats()
	if st.Total != 6 {
		t.Fatalf("total = %d, want 6", st.Total)
	}
	if st.Errors != 4 {
		t.Errorf("errors = %d, want 4", st.Errors)
	}
	if st.Timeouts != 1 {
		t.Errorf("timeouts = %d, want 1", st.Timeouts)
	}
	if want := 4.0 / 6.0; st.ErrorRate < want-1e-9 || st.ErrorRate > want+1e-9 {
		t.Errorf("errorRate = %v, want %v", st.ErrorRate, want)
	}
	if st.StatusCounts[200] != 2 || st.StatusCounts[500] != 1 || st.StatusCounts[404] != 1 {
		t.Errorf("statusCounts = %v", st.StatusCounts)
	}
	// status 0 (timeout/transport) is not counted, matching Collector.
	if _, ok := st.StatusCounts[0]; ok {
		t.Errorf("statusCounts must skip code 0, got %v", st.StatusCounts)
	}
	// Percentiles are estimated from latencies {10,12,30,0,5,0}; max is exact.
	if st.Max != 30 {
		t.Errorf("max = %v, want 30 (exact)", st.Max)
	}
	if st.P50 < 0 || st.P99 < st.P50 {
		t.Errorf("implausible percentiles: p50=%v p99=%v", st.P50, st.P99)
	}
}

// TestSummaryStatsMatchesCollector feeds the same uniform data into a Collector
// and a Summary and asserts the rendered Stats agree (percentiles within the
// histogram tolerance, counters exactly).
func TestSummaryStatsMatchesCollector(t *testing.T) {
	c := NewCollector()
	s := NewSummary()
	for i := 1; i <= 10000; i++ {
		v := float64(i)
		c.Record(200, v, "")
		s.Add(obsv(200, v, "", false))
	}
	cs := c.Snapshot()
	ss := s.Stats()

	if ss.Total != cs.Total || ss.Errors != cs.Errors {
		t.Fatalf("counters differ: summary=%+v collector=%+v", ss, cs)
	}
	assertClose(t, "p50", ss.P50, cs.P50, histTolerance)
	assertClose(t, "p95", ss.P95, cs.P95, histTolerance)
	assertClose(t, "p99", ss.P99, cs.P99, histTolerance)
	if ss.Max != cs.Max {
		t.Errorf("max: summary=%v collector=%v", ss.Max, cs.Max)
	}
}

func TestSummaryMergeCounters(t *testing.T) {
	a := NewSummary()
	b := NewSummary()

	a.Add(obsv(200, 10, "", false))
	a.Add(obsv(500, 20, "", false))        // error+contract+availability
	a.Add(obsv(0, 0, TimeoutClass, false)) // error+timeout+availability

	b.Add(obsv(200, 11, "", false))
	b.Add(obsv(404, 7, "", false)) // error+threshold
	b.Add(obsv(503, 99, "", true)) // mutated failure -> mutation + availability

	a.Merge(b)
	st := a.Stats()

	if st.Total != 6 {
		t.Fatalf("merged total = %d, want 6", st.Total)
	}
	// errors: a has 2, b has 2 -> 4.
	if st.Errors != 4 {
		t.Errorf("merged errors = %d, want 4", st.Errors)
	}
	if st.Timeouts != 1 {
		t.Errorf("merged timeouts = %d, want 1", st.Timeouts)
	}
	if st.StatusCounts[200] != 2 {
		t.Errorf("merged statusCounts[200] = %d, want 2", st.StatusCounts[200])
	}
	if st.StatusCounts[500] != 1 || st.StatusCounts[404] != 1 || st.StatusCounts[503] != 1 {
		t.Errorf("merged statusCounts = %v", st.StatusCounts)
	}

	// Finding tallies must sum across the merge.
	fc := a.FindingCounts()
	// availability signals: 500, timeout (from a) + 503 (from b) = 3.
	if fc[domain.FindingAvailability] != 3 {
		t.Errorf("availability tally = %d, want 3", fc[domain.FindingAvailability])
	}
	// mutation: only the mutated 503 in b.
	if fc[domain.FindingMutation] != 1 {
		t.Errorf("mutation tally = %d, want 1", fc[domain.FindingMutation])
	}
	// contract: non-mutated 5xx -> the 500 in a (the 503 is mutated, excluded).
	if fc[domain.FindingContract] != 1 {
		t.Errorf("contract tally = %d, want 1", fc[domain.FindingContract])
	}
}

// TestSummaryMergeEquivalence: merging two summaries equals one summary fed all
// observations (counters exact, percentiles identical bucket math).
func TestSummaryMergeEquivalence(t *testing.T) {
	a := NewSummary()
	b := NewSummary()
	whole := NewSummary()

	for i := 1; i <= 4000; i++ {
		o := obsv(200, float64(i), "", false)
		a.Add(o)
		whole.Add(o)
	}
	for i := 0; i < 4000; i++ {
		o := obsv(200, 1000+float64(i)*2, "", false)
		b.Add(o)
		whole.Add(o)
	}
	a.Merge(b)

	as, ws := a.Stats(), whole.Stats()
	if as.Total != ws.Total {
		t.Fatalf("total: merged=%d whole=%d", as.Total, ws.Total)
	}
	// Same underlying bucket counts => identical quantile estimates.
	if as.P50 != ws.P50 || as.P95 != ws.P95 || as.P99 != ws.P99 {
		t.Errorf("percentiles diverged: merged{p50:%v p95:%v p99:%v} whole{p50:%v p95:%v p99:%v}",
			as.P50, as.P95, as.P99, ws.P50, ws.P95, ws.P99)
	}
	if as.Max != ws.Max {
		t.Errorf("max: merged=%v whole=%v", as.Max, ws.Max)
	}
}

func TestSummaryMergeNil(t *testing.T) {
	s := NewSummary()
	s.Add(obsv(200, 5, "", false))
	s.Merge(nil) // must not panic
	if s.Stats().Total != 1 {
		t.Fatalf("total = %d after nil merge, want 1", s.Stats().Total)
	}
}

func TestSummaryStatsCopiesStatusCounts(t *testing.T) {
	s := NewSummary()
	s.Add(obsv(200, 1, "", false))
	st := s.Stats()
	st.StatusCounts[200] = 999 // mutate the returned map
	if s.Stats().StatusCounts[200] != 1 {
		t.Error("Stats().StatusCounts should be a defensive copy")
	}
	// FindingCounts is likewise a copy.
	s.Add(obsv(500, 1, "", false))
	fc := s.FindingCounts()
	fc[domain.FindingContract] = 999
	if s.FindingCounts()[domain.FindingContract] != 1 {
		t.Error("FindingCounts should be a defensive copy")
	}
}

func TestSummaryEmpty(t *testing.T) {
	st := NewSummary().Stats()
	if st.Total != 0 || st.Errors != 0 || st.ErrorRate != 0 || st.P50 != 0 || st.P95 != 0 || st.P99 != 0 || st.Max != 0 {
		t.Fatalf("empty summary not zero-valued: %+v", st)
	}
	if st.StatusCounts == nil {
		t.Error("StatusCounts should be non-nil even when empty")
	}
}

// TestSummaryConcurrentAdd exercises Add from many goroutines under -race and
// asserts exact counts (no lost updates), validating the documented
// concurrency model.
func TestSummaryConcurrentAdd(t *testing.T) {
	s := NewSummary()
	const goroutines, each = 32, 1000
	var wantErrors int64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// Deterministic mix: every 5th request is a 500 error.
				if i%5 == 0 {
					s.Add(obsv(500, float64(i%100+1), "", false))
					atomic.AddInt64(&wantErrors, 1)
				} else {
					s.Add(obsv(200, float64(i%100+1), "", false))
				}
			}
		}(g)
	}
	wg.Wait()

	st := s.Stats()
	if st.Total != goroutines*each {
		t.Fatalf("total = %d, want %d (lost updates)", st.Total, goroutines*each)
	}
	if int64(st.Errors) != atomic.LoadInt64(&wantErrors) {
		t.Errorf("errors = %d, want %d", st.Errors, wantErrors)
	}
	errs := goroutines * (each / 5)
	if st.StatusCounts[500] != errs || st.StatusCounts[200] != goroutines*each-errs {
		t.Errorf("statusCounts = %v, want 500:%d 200:%d", st.StatusCounts, errs, goroutines*each-errs)
	}
}

// TestSummaryConcurrentMergeWhileAdd verifies Merge can snapshot a summary that
// is being concurrently Added to without a race (run under -race).
func TestSummaryConcurrentMergeWhileAdd(t *testing.T) {
	src := NewSummary()
	dst := NewSummary()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				src.Add(obsv(200, 3, "", false))
			}
		}
	}()
	for i := 0; i < 200; i++ {
		dst.Merge(src) // races against the adder goroutine if locking is wrong
	}
	close(stop)
	wg.Wait()
	// No assertion on exact totals (timing-dependent); -race is the oracle.
	if dst.Stats().Total < 0 {
		t.Fatal("impossible")
	}
}
