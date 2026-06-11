package report

import (
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// bf builds a finding with just the fields that matter for baseline diffing.
func bf(cat domain.FindingCategory, sev domain.Severity, ref, desc string) domain.Finding {
	return domain.Finding{Category: cat, Severity: sev, EvidenceRef: ref, Description: desc}
}

// TestDiffAgainstBaselineBuckets pins the three-way split a regression gate
// needs: a finding only in the current run is new, only in the baseline is
// resolved, and in both is persisting — keyed by (category, evidenceRef), the
// same identity the comparison view uses.
func TestDiffAgainstBaselineBuckets(t *testing.T) {
	baseline := []domain.Finding{
		bf(domain.FindingContract, domain.SeverityCritical, "orders", "2 contract violation(s) on orders"),
		bf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.31 exceeded threshold 0.20"),
	}
	current := []domain.Finding{
		bf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.44 exceeded threshold 0.20"),
		bf(domain.FindingAvailability, domain.SeverityCritical, "checkout", "6 consecutive failures on checkout"),
	}

	d := DiffAgainstBaseline(baseline, current)

	if len(d.New) != 1 || d.New[0].EvidenceRef != "checkout" {
		t.Errorf("New = %+v, want exactly the checkout availability finding", d.New)
	}
	if len(d.Resolved) != 1 || d.Resolved[0].EvidenceRef != "orders" {
		t.Errorf("Resolved = %+v, want exactly the orders contract finding", d.Resolved)
	}
	if len(d.Persisting) != 1 || d.Persisting[0].EvidenceRef != "error-rate" {
		t.Fatalf("Persisting = %+v, want exactly the error-rate threshold finding", d.Persisting)
	}
	// The persisting bucket must carry the current run's occurrence: its
	// description holds this run's numbers, which is what a CI table shows.
	if got := d.Persisting[0].Description; got != "error rate 0.44 exceeded threshold 0.20" {
		t.Errorf("Persisting carries %q, want the current run's description", got)
	}
}

// TestDiffAgainstBaselineCountChangesAreNotNew re-pins the key stability that
// makes the gate trustworthy: the same issue whose description differs only by
// run-specific numbers must never classify as new — a gate that reddens on
// every count fluctuation would be ignored within a week.
func TestDiffAgainstBaselineCountChangesAreNotNew(t *testing.T) {
	baseline := []domain.Finding{
		bf(domain.FindingContract, domain.SeverityCritical, "checkout", "3 contract violation(s) on checkout"),
		bf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.31 exceeded threshold 0.20"),
	}
	current := []domain.Finding{
		bf(domain.FindingContract, domain.SeverityCritical, "checkout", "7 contract violation(s) on checkout"),
		bf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.44 exceeded threshold 0.20"),
	}

	d := DiffAgainstBaseline(baseline, current)
	if len(d.New) != 0 || len(d.Resolved) != 0 {
		t.Fatalf("count-only differences must not split into new/resolved: new=%+v resolved=%+v", d.New, d.Resolved)
	}
	if len(d.Persisting) != 2 {
		t.Errorf("want 2 persisting findings, got %d: %+v", len(d.Persisting), d.Persisting)
	}
}

// TestDiffAgainstBaselineSortsBySeverity pins deterministic, most-severe-first
// ordering in every bucket so the CI table renders stably across runs.
func TestDiffAgainstBaselineSortsBySeverity(t *testing.T) {
	current := []domain.Finding{
		bf(domain.FindingThreshold, domain.SeverityWarning, "p95-latency", "p95 latency 900.0ms exceeded threshold 500.0ms"),
		bf(domain.FindingContract, domain.SeverityCritical, "orders", "2 contract violation(s) on orders"),
	}

	d := DiffAgainstBaseline(nil, current)
	if len(d.New) != 2 {
		t.Fatalf("want 2 new findings, got %d", len(d.New))
	}
	if d.New[0].Severity != domain.SeverityCritical || d.New[1].Severity != domain.SeverityWarning {
		t.Errorf("New not sorted most-severe first: %+v", d.New)
	}
}
