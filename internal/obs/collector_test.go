package obs

import (
	"math"
	"sync"
	"testing"
)

func TestPercentilesApprox(t *testing.T) {
	c := NewCollector()
	// latencies 1..100 ms, all 200 OK.
	for i := 1; i <= 100; i++ {
		c.Record(200, float64(i), "")
	}
	s := c.Snapshot()
	// Percentiles come from the bounded HDR histogram, so they land within its
	// ~1% relative bucket error of the true value rather than exactly on it.
	approx := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > want*0.02 {
			t.Errorf("%s = %v, want ~%v (within 2%%)", name, got, want)
		}
	}
	approx("p50", s.P50, 50)
	approx("p95", s.P95, 95)
	approx("p99", s.P99, 99)
	// Max is tracked exactly, not bucketed.
	if s.Max != 100 {
		t.Errorf("max = %v, want 100 (exact)", s.Max)
	}
}

// TestRecordSanitizesLatency feeds non-finite and negative latencies alongside
// good ones. A NaN makes sort order undefined and a negative value would drag
// percentiles below zero, so Record must clamp them to 0 before storing. The
// request still counts toward total/errors, only the stored latency is fixed.
func TestRecordSanitizesLatency(t *testing.T) {
	c := NewCollector()
	c.Record(200, 10, "")
	c.Record(200, math.NaN(), "")
	c.Record(200, 20, "")
	c.Record(200, -5, "")
	c.Record(200, math.Inf(1), "")
	c.Record(200, math.Inf(-1), "")

	s := c.Snapshot()
	if s.Total != 6 {
		t.Fatalf("total = %d, want 6", s.Total)
	}
	// Every percentile and the max must be finite and non-negative despite the
	// poisoned inputs.
	for _, p := range []struct {
		name string
		val  float64
	}{{"p50", s.P50}, {"p95", s.P95}, {"p99", s.P99}, {"max", s.Max}} {
		if math.IsNaN(p.val) || math.IsInf(p.val, 0) {
			t.Errorf("%s = %v, want finite", p.name, p.val)
		}
		if p.val < 0 {
			t.Errorf("%s = %v, want non-negative", p.name, p.val)
		}
	}
	// The two finite good samples (10, 20) survive; the rest clamp to 0, so the
	// max stays the largest real latency.
	if s.Max != 20 {
		t.Errorf("max = %v, want 20", s.Max)
	}
}

func TestErrorRateAndTimeouts(t *testing.T) {
	c := NewCollector()
	c.Record(200, 10, "")
	c.Record(200, 12, "")
	c.Record(500, 30, "")              // error (5xx)
	c.Record(0, 0, TimeoutClass)       // error + timeout
	c.Record(404, 5, "")               // error (4xx)
	c.Record(0, 0, "connection reset") // error (transport)

	s := c.Snapshot()
	if s.Total != 6 {
		t.Fatalf("total = %d, want 6", s.Total)
	}
	if s.Errors != 4 {
		t.Errorf("errors = %d, want 4", s.Errors)
	}
	if s.Timeouts != 1 {
		t.Errorf("timeouts = %d, want 1", s.Timeouts)
	}
	if got, want := s.ErrorRate, 4.0/6.0; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("errorRate = %v, want %v", got, want)
	}
}

func TestStatusDistribution(t *testing.T) {
	c := NewCollector()
	c.Record(200, 1, "")
	c.Record(200, 1, "")
	c.Record(503, 1, "")
	s := c.Snapshot()
	if s.StatusCounts[200] != 2 || s.StatusCounts[503] != 1 {
		t.Fatalf("status counts = %v", s.StatusCounts)
	}
	// Snapshot returns a copy: mutating it must not affect the collector.
	s.StatusCounts[200] = 999
	if c.Snapshot().StatusCounts[200] != 2 {
		t.Error("snapshot StatusCounts should be a copy")
	}
}

func TestEmptySnapshot(t *testing.T) {
	s := NewCollector().Snapshot()
	if s.Total != 0 || s.ErrorRate != 0 || s.P95 != 0 {
		t.Fatalf("empty snapshot not zero-valued: %+v", s)
	}
}

func TestConcurrentRecord(t *testing.T) {
	c := NewCollector()
	const goroutines, each = 20, 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				c.Record(200, float64(i%50), "")
			}
		}()
	}
	wg.Wait()
	if got := c.Snapshot().Total; got != goroutines*each {
		t.Fatalf("total = %d, want %d", got, goroutines*each)
	}
}
