package obs

import (
	"math"
	"sync"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TimeoutClass is the error class used for requests that timed out.
const TimeoutClass = "timeout"

// Stats is an aggregated snapshot of observed client-side behavior.
type Stats struct {
	Total        int         `json:"total"`
	Errors       int         `json:"errors"`
	Timeouts     int         `json:"timeouts"`
	ErrorRate    float64     `json:"errorRate"`
	StatusCounts map[int]int `json:"statusCounts"`
	P50          float64     `json:"p50"`
	P95          float64     `json:"p95"`
	P99          float64     `json:"p99"`
	Max          float64     `json:"max"`
}

// Collector ingests per-request observations and aggregates them. It is safe for
// concurrent use by many virtual users. Latencies fold into a bounded, HDR-style
// Histogram rather than a growing slice, so a Collector's memory stays flat no
// matter how many requests a run makes (a long high-RPS run previously grew an
// unbounded []float64). It also means a local run's percentiles come from the
// same Histogram the distributed Summary uses, so the two paths agree.
type Collector struct {
	mu           sync.Mutex
	lat          *Histogram
	statusCounts map[int]int
	total        int
	errors       int
	timeouts     int
}

// NewCollector returns an empty collector.
func NewCollector() *Collector {
	return &Collector{lat: NewHistogram(), statusCounts: make(map[int]int)}
}

// Record ingests one request outcome. A result counts as an error when the
// status is >= 400 or an errorClass is present (e.g. a transport failure); a
// timeout additionally increments the timeout counter.
func (c *Collector) Record(statusCode int, latencyMs float64, errorClass string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total++
	c.lat.Observe(sanitizeLatency(latencyMs))
	if statusCode > 0 {
		c.statusCounts[statusCode]++
	}
	if errorClass == TimeoutClass {
		c.timeouts++
	}
	if statusCode >= 400 || errorClass != "" {
		c.errors++
	}
}

// sanitizeLatency clamps a latency that cannot meaningfully enter the percentile
// math to 0: a negative value would drag percentiles below zero and a NaN/±Inf
// would poison the aggregate. This matches how the Histogram and Summary treat
// degenerate samples. The request still counts toward total/errors/status; only
// the stored latency is fixed up.
func sanitizeLatency(latencyMs float64) float64 {
	if math.IsNaN(latencyMs) || latencyMs < 0 || math.IsInf(latencyMs, 0) {
		return 0
	}
	return latencyMs
}

// RecordSample ingests a domain MetricSample.
func (c *Collector) RecordSample(s domain.MetricSample) {
	c.Record(s.StatusCode, s.LatencyMs, s.ErrorClass)
}

// Snapshot computes the current aggregate. The returned StatusCounts is a copy.
// Quantiles read from the bounded histogram in O(buckets) — cheap enough to run
// under the lock, replacing the old O(n log n) sort of a growing slice. Max is
// tracked exactly; the percentiles carry the histogram's ~1% bucket error.
func (c *Collector) Snapshot() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Stats{
		Total:        c.total,
		Errors:       c.errors,
		Timeouts:     c.timeouts,
		StatusCounts: make(map[int]int, len(c.statusCounts)),
	}
	for k, v := range c.statusCounts {
		st.StatusCounts[k] = v
	}
	if st.Total > 0 {
		st.ErrorRate = float64(st.Errors) / float64(st.Total)
	}
	if c.lat.Count() > 0 {
		st.P50 = c.lat.Quantile(0.50)
		st.P95 = c.lat.Quantile(0.95)
		st.P99 = c.lat.Quantile(0.99)
		st.Max = c.lat.Max()
	}
	return st
}

// percentile returns the nearest-rank percentile of a sorted slice. p is in
// [0,1]; the slice must be non-empty and ascending. It is retained for the
// per-endpoint finding classifier (finding.go), which works on a bounded
// per-endpoint sample rather than the whole run.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	rank := int(math.Ceil(p * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}
