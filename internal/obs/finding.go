package obs

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// RequestObservation is one observed request with enough context to classify a
// finding (notably whether the input was deliberately mutated).
type RequestObservation struct {
	APIID      domain.ID
	StatusCode int
	LatencyMs  float64
	ErrorClass string // "", "transport", "timeout", "assertion", ...
	Mutated    bool
	TS         time.Time
}

func (o RequestObservation) failed() bool {
	return o.StatusCode >= 400 || o.ErrorClass != ""
}

func (o RequestObservation) unavailable() bool {
	return o.StatusCode >= 500 || o.ErrorClass == "timeout" || o.ErrorClass == "transport"
}

// contractSignal reports whether o is a contract violation: a non-mutated
// happy-path request that returned a 5xx or failed an assertion. Shared by the
// obs-list classifier (classifyContract) and the Summary tally so the predicate
// cannot drift between the two paths.
func (o RequestObservation) contractSignal() bool {
	return !o.Mutated && (o.StatusCode >= 500 || o.ErrorClass == "assertion")
}

// mutationSignal reports whether o is a mutation finding: a mutated input that
// surfaced an error. Shared by the obs-list classifier (classifyMutation) and
// the Summary tally so the predicate cannot drift between the two paths.
func (o RequestObservation) mutationSignal() bool {
	return o.Mutated && o.failed()
}

// ClassifyConfig holds the thresholds used to turn observations into findings.
type ClassifyConfig struct {
	ErrorRateThreshold float64 // overall error rate above this -> threshold finding
	P95LatencyMs       float64 // overall p95 latency above this -> threshold finding (0 disables)
	AvailabilityRun    int     // this many consecutive failures on an API -> availability finding
}

// Aggregator collects observations and classifies findings across four
// categories: threshold, contract, mutation and availability.
type Aggregator struct {
	mu  sync.Mutex
	obs []RequestObservation
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator { return &Aggregator{} }

// Add records one observation (safe for concurrent use).
func (a *Aggregator) Add(o RequestObservation) {
	a.mu.Lock()
	a.obs = append(a.obs, o)
	a.mu.Unlock()
}

// Classify returns the findings for runID under cfg. Findings are grouped per
// API per category so one bad endpoint yields one finding, not hundreds.
func (a *Aggregator) Classify(runID domain.ID, cfg ClassifyConfig) []domain.Finding {
	a.mu.Lock()
	obs := make([]RequestObservation, len(a.obs))
	copy(obs, a.obs)
	a.mu.Unlock()

	var findings []domain.Finding
	findings = append(findings, classifyMutation(runID, obs)...)
	findings = append(findings, classifyContract(runID, obs)...)
	findings = append(findings, classifyAvailability(runID, obs, cfg.AvailabilityRun)...)
	findings = append(findings, classifyThreshold(runID, obs, cfg)...)
	return findings
}

func classifyMutation(runID domain.ID, obs []RequestObservation) []domain.Finding {
	counts := map[domain.ID]int{}
	firstSeen := map[domain.ID]time.Time{}
	for _, o := range obs {
		if o.mutationSignal() {
			counts[o.APIID]++
			if _, ok := firstSeen[o.APIID]; !ok {
				firstSeen[o.APIID] = o.TS
			}
		}
	}
	return findingsFromCounts(runID, domain.FindingMutation, domain.SeverityWarning, counts, firstSeen,
		"mutated input surfaced %d error(s) on %s")
}

func classifyContract(runID domain.ID, obs []RequestObservation) []domain.Finding {
	// A non-mutated request that gets a 5xx or fails an assertion is a contract
	// issue: the happy path produced an error a developer likely missed.
	counts := map[domain.ID]int{}
	firstSeen := map[domain.ID]time.Time{}
	for _, o := range obs {
		if o.contractSignal() {
			counts[o.APIID]++
			if _, ok := firstSeen[o.APIID]; !ok {
				firstSeen[o.APIID] = o.TS
			}
		}
	}
	return findingsFromCounts(runID, domain.FindingContract, domain.SeverityCritical, counts, firstSeen,
		"%d contract violation(s) on %s (unexpected error on the happy path)")
}

// classifyAvailability flags APIs that suffered a long enough run of consecutive
// unavailable() results. The streak is evaluated in per-endpoint timestamp order
// (o.TS), so it no longer depends on the interleave in which concurrently-streamed
// results happened to be recorded: the same multiset of observations yields the
// same finding regardless of arrival order. Observations sharing an equal TS keep
// their insertion order (stable tie-break), which preserves the behaviour of the
// zero-TS / monotonic-TS test fixtures while making distinctly-timed events robust.
func classifyAvailability(runID domain.ID, obs []RequestObservation, run int) []domain.Finding {
	if run <= 0 {
		return nil
	}
	// Group by API in first-seen order, then sort each group by completion time.
	byAPI := map[domain.ID][]RequestObservation{}
	order := make([]domain.ID, 0)
	for _, o := range obs {
		if _, ok := byAPI[o.APIID]; !ok {
			order = append(order, o.APIID)
		}
		byAPI[o.APIID] = append(byAPI[o.APIID], o)
	}
	maxRun := map[domain.ID]int{}
	for _, api := range order {
		group := byAPI[api]
		sort.SliceStable(group, func(i, j int) bool { return group[i].TS.Before(group[j].TS) })
		cur := 0
		for _, o := range group {
			if o.unavailable() {
				cur++
				if cur > maxRun[api] {
					maxRun[api] = cur
				}
			} else {
				cur = 0
			}
		}
	}
	counts := map[domain.ID]int{}
	for api, m := range maxRun {
		if m >= run {
			counts[api] = m
		}
	}
	return findingsFromCounts(runID, domain.FindingAvailability, domain.SeverityCritical, counts, nil,
		"%d consecutive failures on %s (saturation or downtime)")
}

func classifyThreshold(runID domain.ID, obs []RequestObservation, cfg ClassifyConfig) []domain.Finding {
	var failed, total int
	latencies := make([]float64, 0, len(obs))
	for _, o := range obs {
		if o.Mutated {
			continue // mutation testing deliberately fails; not a threshold signal
		}
		total++
		if o.failed() {
			failed++
		}
		latencies = append(latencies, o.LatencyMs)
	}
	if total == 0 {
		return nil
	}
	rate := float64(failed) / float64(total)
	// Only sort + compute p95 when the p95 gate is enabled (preserves the
	// sort-only-when-needed behaviour); pass p95=0 when disabled.
	var p95 float64
	if cfg.P95LatencyMs > 0 {
		sort.Float64s(latencies)
		p95 = percentile(latencies, 0.95)
	}
	return thresholdFindings(runID, rate, p95, cfg)
}

// thresholdFindings builds the 0-2 threshold findings shared by the obs-list and
// Summary classifiers, so their messages and comparisons cannot drift.
func thresholdFindings(runID domain.ID, errorRate, p95 float64, cfg ClassifyConfig) []domain.Finding {
	var out []domain.Finding
	if cfg.ErrorRateThreshold > 0 && errorRate > cfg.ErrorRateThreshold {
		out = append(out, domain.Finding{RunID: runID, Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
			Description: fmt.Sprintf("error rate %.2f exceeded threshold %.2f", errorRate, cfg.ErrorRateThreshold)})
	}
	if cfg.P95LatencyMs > 0 && p95 > cfg.P95LatencyMs {
		out = append(out, domain.Finding{RunID: runID, Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
			Description: fmt.Sprintf("p95 latency %.1fms exceeded threshold %.1fms", p95, cfg.P95LatencyMs)})
	}
	return out
}

// findingsFromCounts builds one finding per API present in counts, in stable
// API-id order, using a format string of (count, apiID). firstSeen supplies the
// per-API first-occurrence timestamp (may be nil to leave it zero).
func findingsFromCounts(runID domain.ID, cat domain.FindingCategory, sev domain.Severity, counts map[domain.ID]int, firstSeen map[domain.ID]time.Time, format string) []domain.Finding {
	if len(counts) == 0 {
		return nil
	}
	apis := make([]domain.ID, 0, len(counts))
	for api := range counts {
		apis = append(apis, api)
	}
	sort.Slice(apis, func(i, j int) bool { return apis[i] < apis[j] })

	out := make([]domain.Finding, 0, len(apis))
	for _, api := range apis {
		out = append(out, domain.Finding{
			RunID:       runID,
			Category:    cat,
			Severity:    sev,
			EvidenceRef: string(api),
			FirstSeen:   firstSeen[api],
			Description: fmt.Sprintf(format, counts[api], api),
		})
	}
	return out
}
