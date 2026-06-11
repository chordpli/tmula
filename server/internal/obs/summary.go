package obs

import (
	"fmt"
	"sync"

	"github.com/chordpli/tmula/server/internal/domain"
)

// Summary is a compact, mergeable aggregate of a run that retains NO per-sample
// data. It keeps running counters plus a bounded-memory latency Histogram, so a
// distributed worker can fold millions of requests into a fixed-size value and
// ship that instead of streaming every observation. The master then Merges the
// per-worker summaries and renders the existing Stats shape.
//
// One deliberate difference from the single-node Aggregator: a Summary's
// total/errors/histogram — and therefore the threshold error-rate and p95
// findings derived in FindingsFromSummary — count ALL observations, including
// mutated requests, whereas Aggregator.classifyThreshold excludes mutated
// requests. So on a run that carries mutated observations those two threshold
// findings can differ between the two paths. (It is latent today because the
// only FindingsFromSummary caller is the distributed path, where observations
// are always Mutated=false.)
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
// histogram.
//
// Note that total/errors/the histogram count EVERY observation, including
// mutated requests, whereas the single-node Aggregator's threshold error-rate
// and p95 deliberately exclude mutated requests (mutation testing fails on
// purpose). So on a mutation-bearing run the threshold/p95 signals a Summary
// surfaces can differ from the Aggregator's — see FindingsFromSummary. The
// per-category finding tallies kept here follow the Aggregator's classifiers.
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
// The mutation/contract/availability predicates mirror obs/finding.go's
// classifiers. The threshold signals derived later (error-rate and p95 in
// FindingsFromSummary) come from total/errors/the histogram, which count all
// observations including mutated ones — unlike Aggregator.classifyThreshold,
// which excludes mutated requests — so those two can diverge from the
// single-node Aggregator on a mutation-bearing run.
func (s *Summary) tallyFindings(o RequestObservation) {
	switch {
	case o.Mutated:
		// Mutation testing: a mutated input that errors is a mutation signal.
		if o.mutationSignal() {
			s.findingCounts[domain.FindingMutation]++
		}
	default:
		// Non-mutated happy-path request: a 5xx or assertion failure is a
		// contract violation; non-mutated failures also feed the threshold
		// error-rate signal.
		if o.contractSignal() {
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

// SummaryData is the exported, plain-data form of a Summary: every field needed
// to serialize it across a wire and rebuild a mergeable Summary on the far side.
// The histogram travels as its fixed-layout bucket counts plus the exact max.
type SummaryData struct {
	Total         int
	Errors        int
	Timeouts      int
	StatusCounts  map[int]int
	FindingCounts map[domain.FindingCategory]int
	HistBuckets   []uint64
	HistMax       float64
}

// Export returns the summary as plain data, taken under the lock, ready to
// serialize. LoadSummary is its inverse.
func (s *Summary) Export() SummaryData {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := SummaryData{
		Total:         s.total,
		Errors:        s.errors,
		Timeouts:      s.timeouts,
		StatusCounts:  make(map[int]int, len(s.statusCounts)),
		FindingCounts: make(map[domain.FindingCategory]int, len(s.findingCounts)),
		HistBuckets:   s.hist.Buckets(),
		HistMax:       s.hist.Max(),
	}
	for k, v := range s.statusCounts {
		d.StatusCounts[k] = v
	}
	for k, v := range s.findingCounts {
		d.FindingCounts[k] = v
	}
	return d
}

// LoadSummary rebuilds a Summary from exported data (e.g. decoded off the wire)
// so it can be Merged with locally built summaries. It errors if the histogram
// buckets do not match the fixed layout.
func LoadSummary(d SummaryData) (*Summary, error) {
	h, err := LoadHistogram(d.HistBuckets, d.HistMax)
	if err != nil {
		return nil, err
	}
	s := &Summary{
		hist:          h,
		total:         d.Total,
		errors:        d.Errors,
		timeouts:      d.Timeouts,
		statusCounts:  make(map[int]int, len(d.StatusCounts)),
		findingCounts: make(map[domain.FindingCategory]int, len(d.FindingCounts)),
	}
	for k, v := range d.StatusCounts {
		s.statusCounts[k] = v
	}
	for k, v := range d.FindingCounts {
		s.findingCounts[k] = v
	}
	return s, nil
}

// FindingsFromSummary derives run-wide findings from a (merged) Summary. Because
// a Summary keeps only category tallies — no per-API breakdown and no ordering —
// these are deliberately coarser than the Aggregator's per-endpoint, run-length
// findings: at most one finding per tripped category for the whole run. It is the
// classification the worker-aggregated distributed path uses, where trading that
// fidelity for bounded memory is the entire point. The category order matches
// Aggregator.Classify (mutation, contract, availability, threshold).
//
// The threshold signals here (error rate and p95) are computed from the
// Summary's Stats, i.e. from total/errors/the histogram, which count ALL
// observations including mutated requests. Aggregator.classifyThreshold instead
// excludes mutated requests, so on a mutation-bearing run these two threshold
// findings can differ from the single-node Aggregator's. This is latent on the
// only current caller (the distributed path, where observations are always
// Mutated=false) but is the correct contract once mutated observations can reach
// a Summary.
func FindingsFromSummary(runID domain.ID, s *Summary, cfg ClassifyConfig) []domain.Finding {
	st := s.Stats()
	counts := s.FindingCounts()
	var out []domain.Finding
	// The coarse findings have no per-API identity, so they all carry the
	// run-wide evidence ref: stable and non-empty, which is what the run
	// comparison's (category, evidenceRef) key needs.
	if n := counts[domain.FindingMutation]; n > 0 {
		out = append(out, domain.Finding{
			RunID: runID, Category: domain.FindingMutation, Severity: domain.SeverityWarning,
			EvidenceRef: evidenceRunWide, Count: n,
			Description: fmt.Sprintf("mutated input surfaced %d error(s) across the run", n),
		})
	}
	if n := counts[domain.FindingContract]; n > 0 {
		out = append(out, domain.Finding{
			RunID: runID, Category: domain.FindingContract, Severity: domain.SeverityCritical,
			EvidenceRef: evidenceRunWide, Count: n,
			Description: fmt.Sprintf("%d contract violation(s) across the run (unexpected error on the happy path)", n),
		})
	}
	if n := counts[domain.FindingAvailability]; cfg.AvailabilityRun > 0 && n >= cfg.AvailabilityRun {
		out = append(out, domain.Finding{
			RunID: runID, Category: domain.FindingAvailability, Severity: domain.SeverityCritical,
			EvidenceRef: evidenceRunWide, Count: n,
			Description: fmt.Sprintf("%d unavailable response(s) across the run (saturation or downtime)", n),
		})
	}
	// Threshold findings come last (after mutation/contract/availability). Error
	// rate/p95 come from st (all observations) — the documented divergence from
	// Aggregator.classifyThreshold's mutated-exclusion is intentional; the shared
	// builder only guarantees the messages and comparisons cannot drift.
	out = append(out, thresholdFindings(runID, st.ErrorRate, st.P95, cfg)...)
	return out
}
