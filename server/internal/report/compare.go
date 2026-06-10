package report

import (
	"fmt"
	"sort"

	"github.com/chordpli/tmula/server/internal/domain"
)

// compareView is the presentation-ready shape the comparison template renders:
// the two run headers, a row of metric deltas, and the findings diff split into
// new / resolved / persisting buckets.
type compareView struct {
	A         runHeader
	B         runHeader
	Metrics   []metricDelta
	New       []domain.Finding // present only in B (regressed/introduced)
	Resolved  []domain.Finding // present only in A (fixed/gone)
	Persisted []findingPair    // present in both
}

// runHeader is the identifying line for one side of the comparison.
type runHeader struct {
	ID      domain.ID
	Status  domain.RunStatus
	Mode    domain.RunMode
	Workers int
}

// metricDelta is one metric compared across the two runs, with a formatted
// percent change and a direction class so the template can color regressions.
type metricDelta struct {
	Name   string
	A      string // formatted value for run A
	B      string // formatted value for run B
	Change string // e.g. "+50.0%", "-12.5%", "n/a", "0.0%"
	// Dir is "up", "down" or "flat": the direction B moved relative to A.
	// Higher latency/error counts are worse, so "up" is the regression class.
	Dir string
}

// findingPair is a finding seen in both runs, carrying both occurrences so the
// template can show each side's first-seen time if it differs.
type findingPair struct {
	A domain.Finding
	B domain.Finding
}

func newCompareView(a, b Data) compareView {
	return compareView{
		A:         headerFor(a),
		B:         headerFor(b),
		Metrics:   metricDeltas(a, b),
		New:       sortFindings(diffFindings(b.Findings, a.Findings)),
		Resolved:  sortFindings(diffFindings(a.Findings, b.Findings)),
		Persisted: intersectFindings(a.Findings, b.Findings),
	}
}

func headerFor(d Data) runHeader {
	return runHeader{
		ID:      d.Run.ID,
		Status:  d.Run.Status,
		Mode:    d.Run.Mode,
		Workers: d.Workers,
	}
}

// metricDeltas builds the comparison rows. Latency percentiles and error-rate
// are formatted as before; the error rate is shown as a fraction (matching the
// stored value) so a 0.02 -> 0.24 jump reads naturally.
func metricDeltas(a, b Data) []metricDelta {
	sa, sb := a.Stats, b.Stats
	return []metricDelta{
		countDelta("total requests", sa.Total, sb.Total),
		rateDelta("error rate", sa.ErrorRate, sb.ErrorRate),
		msDelta("p50 (ms)", sa.P50, sb.P50),
		msDelta("p95 (ms)", sa.P95, sb.P95),
		msDelta("p99 (ms)", sa.P99, sb.P99),
		msDelta("max (ms)", sa.Max, sb.Max),
	}
}

func msDelta(name string, a, b float64) metricDelta {
	return metricDelta{
		Name:   name,
		A:      fmt.Sprintf("%.1f", a),
		B:      fmt.Sprintf("%.1f", b),
		Change: pctChange(a, b),
		Dir:    direction(a, b),
	}
}

func rateDelta(name string, a, b float64) metricDelta {
	return metricDelta{
		Name:   name,
		A:      fmt.Sprintf("%.2f", a),
		B:      fmt.Sprintf("%.2f", b),
		Change: pctChange(a, b),
		Dir:    direction(a, b),
	}
}

func countDelta(name string, a, b int) metricDelta {
	return metricDelta{
		Name:   name,
		A:      fmt.Sprintf("%d", a),
		B:      fmt.Sprintf("%d", b),
		Change: pctChange(float64(a), float64(b)),
		Dir:    direction(float64(a), float64(b)),
	}
}

// pctChange formats the percent change from a to b. With a zero baseline a
// nonzero b has no meaningful percentage, so it reports "new" rather than an
// infinity; an unchanged zero reports "0.0%".
func pctChange(a, b float64) string {
	if a == 0 {
		if b == 0 {
			return "0.0%"
		}
		return "new"
	}
	return fmt.Sprintf("%+.1f%%", (b-a)/a*100)
}

// direction reports which way b moved relative to a. For every metric we
// compare (latencies, error rate, request count) a higher value is the worse
// outcome, so callers can map "up" to a regression accent.
func direction(a, b float64) string {
	switch {
	case b > a:
		return "up"
	case b < a:
		return "down"
	default:
		return "flat"
	}
}

// findingKey identifies a finding for diffing. Two findings are "the same"
// issue when their category, evidence reference and description match; run id
// and first-seen time are deliberately excluded so the same issue across two
// runs collapses to one key.
type findingKey struct {
	category    domain.FindingCategory
	evidenceRef string
	description string
}

func keyOf(f domain.Finding) findingKey {
	return findingKey{
		category:    f.Category,
		evidenceRef: f.EvidenceRef,
		description: f.Description,
	}
}

// diffFindings returns the findings in want whose key is absent from have.
func diffFindings(want, have []domain.Finding) []domain.Finding {
	present := make(map[findingKey]bool, len(have))
	for _, f := range have {
		present[keyOf(f)] = true
	}
	var out []domain.Finding
	for _, f := range want {
		if !present[keyOf(f)] {
			out = append(out, f)
		}
	}
	return out
}

// intersectFindings returns the findings present in both a and b, pairing each
// A occurrence with its B counterpart. The result is sorted for stable output.
func intersectFindings(a, b []domain.Finding) []findingPair {
	byKey := make(map[findingKey]domain.Finding, len(b))
	for _, f := range b {
		byKey[keyOf(f)] = f
	}
	var pairs []findingPair
	for _, fa := range a {
		if fb, ok := byKey[keyOf(fa)]; ok {
			pairs = append(pairs, findingPair{A: fa, B: fb})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return lessFinding(pairs[i].A, pairs[j].A)
	})
	return pairs
}

// sortFindings orders findings most-severe first, then by category and evidence
// so a diff section renders deterministically.
func sortFindings(fs []domain.Finding) []domain.Finding {
	out := append([]domain.Finding(nil), fs...)
	sort.SliceStable(out, func(i, j int) bool { return lessFinding(out[i], out[j]) })
	return out
}

// severityRank maps a severity to its position in severityOrder; unknown
// severities sort last.
func severityRank(s domain.Severity) int {
	for i, sev := range severityOrder {
		if sev == s {
			return i
		}
	}
	return len(severityOrder)
}

func lessFinding(a, b domain.Finding) bool {
	if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
		return ra < rb
	}
	if a.Category != b.Category {
		return a.Category < b.Category
	}
	if a.EvidenceRef != b.EvidenceRef {
		return a.EvidenceRef < b.EvidenceRef
	}
	return a.Description < b.Description
}
