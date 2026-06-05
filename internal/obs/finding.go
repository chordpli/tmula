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
	var first time.Time
	for _, o := range obs {
		if o.Mutated && o.failed() {
			counts[o.APIID]++
			if first.IsZero() {
				first = o.TS
			}
		}
	}
	return findingsFromCounts(runID, domain.FindingMutation, domain.SeverityWarning, counts, first,
		"mutated input surfaced %d error(s) on %s")
}

func classifyContract(runID domain.ID, obs []RequestObservation) []domain.Finding {
	// A non-mutated request that gets a 5xx or fails an assertion is a contract
	// issue: the happy path produced an error a developer likely missed.
	counts := map[domain.ID]int{}
	var first time.Time
	for _, o := range obs {
		if o.Mutated {
			continue
		}
		if o.StatusCode >= 500 || o.ErrorClass == "assertion" {
			counts[o.APIID]++
			if first.IsZero() {
				first = o.TS
			}
		}
	}
	return findingsFromCounts(runID, domain.FindingContract, domain.SeverityCritical, counts, first,
		"%d contract violation(s) on %s (unexpected error on the happy path)")
}

func classifyAvailability(runID domain.ID, obs []RequestObservation, run int) []domain.Finding {
	if run <= 0 {
		return nil
	}
	maxRun := map[domain.ID]int{}
	cur := map[domain.ID]int{}
	for _, o := range obs {
		if o.unavailable() {
			cur[o.APIID]++
			if cur[o.APIID] > maxRun[o.APIID] {
				maxRun[o.APIID] = cur[o.APIID]
			}
		} else {
			cur[o.APIID] = 0
		}
	}
	counts := map[domain.ID]int{}
	for api, m := range maxRun {
		if m >= run {
			counts[api] = m
		}
	}
	return findingsFromCounts(runID, domain.FindingAvailability, domain.SeverityCritical, counts, time.Time{},
		"%d consecutive failures on %s (saturation or downtime)")
}

func classifyThreshold(runID domain.ID, obs []RequestObservation, cfg ClassifyConfig) []domain.Finding {
	if len(obs) == 0 {
		return nil
	}
	var failed int
	latencies := make([]float64, 0, len(obs))
	for _, o := range obs {
		if o.failed() {
			failed++
		}
		latencies = append(latencies, o.LatencyMs)
	}
	var findings []domain.Finding
	rate := float64(failed) / float64(len(obs))
	if cfg.ErrorRateThreshold > 0 && rate > cfg.ErrorRateThreshold {
		findings = append(findings, domain.Finding{
			RunID: runID, Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
			Description: fmt.Sprintf("error rate %.2f exceeded threshold %.2f", rate, cfg.ErrorRateThreshold),
		})
	}
	if cfg.P95LatencyMs > 0 {
		sort.Float64s(latencies)
		if p95 := percentile(latencies, 0.95); p95 > cfg.P95LatencyMs {
			findings = append(findings, domain.Finding{
				RunID: runID, Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
				Description: fmt.Sprintf("p95 latency %.1fms exceeded threshold %.1fms", p95, cfg.P95LatencyMs),
			})
		}
	}
	return findings
}

// findingsFromCounts builds one finding per API present in counts, in stable
// API-id order, using a format string of (count, apiID).
func findingsFromCounts(runID domain.ID, cat domain.FindingCategory, sev domain.Severity, counts map[domain.ID]int, first time.Time, format string) []domain.Finding {
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
			FirstSeen:   first,
			Description: fmt.Sprintf(format, counts[api], api),
		})
	}
	return out
}
