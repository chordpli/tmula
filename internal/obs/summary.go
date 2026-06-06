package obs

import (
	"sync"

	"github.com/chordpli/tmula/internal/domain"
)

// Summary is a compact, mergeable aggregate of a run that retains NO per-sample
// data. It keeps running counters plus a bounded-memory latency Histogram, so a
// distributed worker can fold millions of requests into a fixed-size value and
// ship that instead of streaming every observation. The master then Merges the
// per-worker summaries and renders the existing Stats shape.
//
// Concurrency model: Summary is safe for concurrent Add from many goroutines
// (it owns an internal mutex, mirroring Collector/Aggregator). Merge and Stats
// also take the lock. The embedded Histogram is single-writer by itself; all of
// its mutation here happens under Summary's lock, so that constraint is upheld.
//
// The zero value is not ready for use; construct with NewSummary.
type Summary struct {
	mu       sync.Mutex
	hist     *Histogram
	total    int
	errors   int
	timeouts int
	// statusCounts tallies observed HTTP status codes (codes <= 0 are skipped,
	// matching Collector).
	statusCounts map[int]int
	// findingCounts tallies observations by the finding category they would
	// contribute to. An observation may count toward several categories (e.g. a
	// mutated failure is both a mutation signal and, if 5xx, unavailable), so
	// these are signal tallies, not a partition of total.
	findingCounts map[domain.FindingCategory]int
}

// NewSummary returns an empty Summary ready to Add into.
func NewSummary() *Summary {
	return &Summary{
		hist:          NewHistogram(),
		statusCounts:  make(map[int]int),
		findingCounts: make(map[domain.FindingCategory]int),
	}
}

// Add folds one observation into the summary. It is safe to call concurrently.
//
// Counting rules match the rest of the package: a result is an error when its
// status is >= 400 or it carries an error class (RequestObservation.failed());
// a timeout additionally bumps the timeout counter. Latency always enters the
// histogram. Finding-category tallies follow the same predicates the Aggregator
// uses to classify, so a Summary's tallies preview which findings a run trips.
func (s *Summary) Add(o RequestObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.total++
	s.hist.Observe(o.LatencyMs)
	if o.StatusCode > 0 {
		s.statusCounts[o.StatusCode]++
	}
	if o.ErrorClass == TimeoutClass {
		s.timeouts++
	}
	if o.failed() {
		s.errors++
	}
	s.tallyFindings(o)
}

// tallyFindings increments per-category signal counts for o. Must hold s.mu.
// The predicates mirror obs/finding.go's classifiers so the tallies line up
// with what Aggregator.Classify would surface.
func (s *Summary) tallyFindings(o RequestObservation) {
	switch {
	case o.Mutated:
		// Mutation testing: a mutated input that errors is a mutation signal.
		if o.failed() {
			s.findingCounts[domain.FindingMutation]++
		}
	default:
		// Non-mutated happy-path request: a 5xx or assertion failure is a
		// contract violation; non-mutated failures also feed the threshold
		// error-rate signal.
		if o.StatusCode >= 500 || o.ErrorClass == "assertion" {
			s.findingCounts[domain.FindingContract]++
		}
		if o.failed() {
			s.findingCounts[domain.FindingThreshold]++
		}
	}
	// Availability is about sustained unavailability; per-observation we count
	// each unavailable result as a contributing signal (run-length detection is
	// the Aggregator's job and needs ordering the Summary deliberately drops).
	if o.unavailable() {
		s.findingCounts[domain.FindingAvailability]++
	}
}

// Merge combines other into s: it sums counters, status counts and finding
// tallies, and merges the latency histograms. The operation is associative and
// commutative, so merging worker summaries in any order yields the same result.
// other is read under its own lock and is not modified.
func (s *Summary) Merge(other *Summary) {
	if other == nil {
		return
	}
	// Snapshot other under its lock to avoid racing a concurrent Add on it.
	o := other.snapshot()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.total += o.total
	s.errors += o.errors
	s.timeouts += o.timeouts
	for code, n := range o.statusCounts {
		s.statusCounts[code] += n
	}
	for cat, n := range o.findingCounts {
		s.findingCounts[cat] += n
	}
	s.hist.Merge(o.hist)
}

// snapshotData is an unlocked, deep-enough copy of a Summary's state for safe
// cross-instance merging.
type snapshotData struct {
	hist          *Histogram
	total         int
	errors        int
	timeouts      int
	statusCounts  map[int]int
	findingCounts map[domain.FindingCategory]int
}

// snapshot returns a copy of s's state taken under its lock. The histogram copy
// is independent so the caller may Merge it without mutating s.
func (s *Summary) snapshot() snapshotData {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := NewHistogram()
	h.Merge(s.hist) // element-wise copy via merge-into-empty
	d := snapshotData{
		hist:          h,
		total:         s.total,
		errors:        s.errors,
		timeouts:      s.timeouts,
		statusCounts:  make(map[int]int, len(s.statusCounts)),
		findingCounts: make(map[domain.FindingCategory]int, len(s.findingCounts)),
	}
	for k, v := range s.statusCounts {
		d.statusCounts[k] = v
	}
	for k, v := range s.findingCounts {
		d.findingCounts[k] = v
	}
	return d
}

// Stats renders the package's standard Stats shape from the summary. Percentiles
// come from the histogram (subject to its documented bucket error bound); Max is
// the histogram's exact tracked maximum. StatusCounts is a fresh copy.
func (s *Summary) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := Stats{
		Total:        s.total,
		Errors:       s.errors,
		Timeouts:     s.timeouts,
		StatusCounts: make(map[int]int, len(s.statusCounts)),
	}
	for k, v := range s.statusCounts {
		st.StatusCounts[k] = v
	}
	if s.total > 0 {
		st.ErrorRate = float64(s.errors) / float64(s.total)
	}
	if s.hist.Count() > 0 {
		st.P50 = s.hist.Quantile(0.50)
		st.P95 = s.hist.Quantile(0.95)
		st.P99 = s.hist.Quantile(0.99)
		st.Max = s.hist.Max()
	}
	return st
}

// FindingCounts returns a copy of the per-category signal tallies. Useful for a
// quick read of which findings a run is trending toward without re-classifying.
func (s *Summary) FindingCounts() map[domain.FindingCategory]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[domain.FindingCategory]int, len(s.findingCounts))
	for k, v := range s.findingCounts {
		out[k] = v
	}
	return out
}
