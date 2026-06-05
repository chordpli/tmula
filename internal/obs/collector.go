package obs

import (
	"math"
	"sort"
	"sync"

	"github.com/chordpli/tmula/internal/domain"
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

// Collector ingests per-request observations and aggregates them. It is safe
// for concurrent use by many virtual users.
type Collector struct {
	mu           sync.Mutex
	latencies    []float64
	statusCounts map[int]int
	total        int
	errors       int
	timeouts     int
}

// NewCollector returns an empty collector.
func NewCollector() *Collector {
	return &Collector{statusCounts: make(map[int]int)}
}

// Record ingests one request outcome. A result counts as an error when the
// status is >= 400 or an errorClass is present (e.g. a transport failure); a
// timeout additionally increments the timeout counter.
func (c *Collector) Record(statusCode int, latencyMs float64, errorClass string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total++
	c.latencies = append(c.latencies, latencyMs)
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

// RecordSample ingests a domain MetricSample.
func (c *Collector) RecordSample(s domain.MetricSample) {
	c.Record(s.StatusCode, s.LatencyMs, s.ErrorClass)
}

// Snapshot computes the current aggregate. The returned StatusCounts is a copy.
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
	if c.total > 0 {
		st.ErrorRate = float64(c.errors) / float64(c.total)
	}
	if len(c.latencies) > 0 {
		sorted := make([]float64, len(c.latencies))
		copy(sorted, c.latencies)
		sort.Float64s(sorted)
		st.P50 = percentile(sorted, 0.50)
		st.P95 = percentile(sorted, 0.95)
		st.P99 = percentile(sorted, 0.99)
		st.Max = sorted[len(sorted)-1]
	}
	return st
}

// percentile returns the nearest-rank percentile of a sorted slice.
// p is in [0,1]. The slice must be non-empty and ascending.
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
