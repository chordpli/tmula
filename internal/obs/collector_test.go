package obs

import (
	"sync"
	"testing"
)

func TestPercentilesNearestRank(t *testing.T) {
	c := NewCollector()
	// latencies 1..100 ms, all 200 OK.
	for i := 1; i <= 100; i++ {
		c.Record(200, float64(i), "")
	}
	s := c.Snapshot()
	if s.P50 != 50 {
		t.Errorf("p50 = %v, want 50", s.P50)
	}
	if s.P95 != 95 {
		t.Errorf("p95 = %v, want 95", s.P95)
	}
	if s.P99 != 99 {
		t.Errorf("p99 = %v, want 99", s.P99)
	}
	if s.Max != 100 {
		t.Errorf("max = %v, want 100", s.Max)
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
