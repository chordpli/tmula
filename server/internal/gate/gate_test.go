package gate

import (
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// gf builds a finding with just the fields the gate cares about.
func gf(cat domain.FindingCategory, sev domain.Severity, ref, desc string) domain.Finding {
	return domain.Finding{Category: cat, Severity: sev, EvidenceRef: ref, Description: desc}
}

func TestParseKnownIssues(t *testing.T) {
	doc := `
- category: threshold
  evidenceRef: error-rate
  reason: shared CI runner is flaky under load (TICKET-123)
  expires: "2026-07-01"
- category: contract
  evidenceRef: checkout
  reason: upstream fix lands next sprint
  expires: 2026-06-20
`
	issues, err := ParseKnownIssues([]byte(doc))
	if err != nil {
		t.Fatalf("ParseKnownIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("want 2 issues, got %d", len(issues))
	}
	first := issues[0]
	if first.Category != "threshold" || first.EvidenceRef != "error-rate" {
		t.Errorf("first issue identity = %q/%q", first.Category, first.EvidenceRef)
	}
	if !strings.Contains(first.Reason, "TICKET-123") {
		t.Errorf("first issue reason = %q", first.Reason)
	}
	if want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC); !first.ExpiresAt.Equal(want) {
		t.Errorf("first issue ExpiresAt = %v, want %v", first.ExpiresAt, want)
	}
	// An unquoted YAML date must parse the same as a quoted one.
	if want := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC); !issues[1].ExpiresAt.Equal(want) {
		t.Errorf("second issue ExpiresAt = %v, want %v", issues[1].ExpiresAt, want)
	}
}

// TestParseKnownIssuesRejectsIncompleteEntries: every field is load-bearing —
// identity to match, reason to justify, expires to force re-triage — so a
// missing one fails parsing rather than silently suppressing forever.
func TestParseKnownIssuesRejectsIncompleteEntries(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string // substring the error must mention
	}{
		{"missing category", "- evidenceRef: x\n  reason: r\n  expires: \"2026-07-01\"\n", "category"},
		{"missing evidenceRef", "- category: threshold\n  reason: r\n  expires: \"2026-07-01\"\n", "evidenceRef"},
		{"missing reason", "- category: threshold\n  evidenceRef: x\n  expires: \"2026-07-01\"\n", "reason"},
		{"missing expires", "- category: threshold\n  evidenceRef: x\n  reason: r\n", "expires"},
		{"bad expires format", "- category: threshold\n  evidenceRef: x\n  reason: r\n  expires: \"July 1\"\n", "expires"},
		{"not yaml", ": not yaml [", "known issues"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseKnownIssues([]byte(c.doc))
			if err == nil {
				t.Fatalf("want error mentioning %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// TestEvaluateSuppressesMatchedNewFinding: a new finding matched by an active
// known issue moves to Suppressed (carrying the issue for display) and must not
// fail the gate.
func TestEvaluateSuppressesMatchedNewFinding(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	current := []domain.Finding{
		gf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.31 exceeded threshold 0.20"),
	}
	known := []KnownIssue{{
		Category:    "threshold",
		EvidenceRef: "error-rate",
		Reason:      "flaky shared runner (TICKET-123)",
		Expires:     "2026-07-01",
		ExpiresAt:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}}

	res := Evaluate(nil, current, known, now)
	if len(res.New) != 0 {
		t.Errorf("matched finding must leave New, got %+v", res.New)
	}
	if len(res.Suppressed) != 1 {
		t.Fatalf("want 1 suppressed finding, got %+v", res.Suppressed)
	}
	if res.Suppressed[0].Finding.EvidenceRef != "error-rate" {
		t.Errorf("suppressed finding = %+v", res.Suppressed[0].Finding)
	}
	if !strings.Contains(res.Suppressed[0].Issue.Reason, "TICKET-123") {
		t.Errorf("suppression must carry its known issue, got %+v", res.Suppressed[0].Issue)
	}
}

// TestEvaluateExpiredSuppressionGatesAgain: an expired entry never suppresses —
// the finding stays New (the gate reddens again) and the entry is reported as
// expired so the operator is told to re-triage, whether or not it matched.
func TestEvaluateExpiredSuppressionGatesAgain(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	current := []domain.Finding{
		gf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.31 exceeded threshold 0.20"),
	}
	known := []KnownIssue{
		{Category: "threshold", EvidenceRef: "error-rate", Reason: "was flaky", Expires: "2026-06-01",
			ExpiresAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		// Expired and matching nothing: still reported, so dead entries get cleaned up.
		{Category: "contract", EvidenceRef: "gone", Reason: "long fixed", Expires: "2026-01-01",
			ExpiresAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	res := Evaluate(nil, current, known, now)
	if len(res.New) != 1 || res.New[0].EvidenceRef != "error-rate" {
		t.Errorf("expired suppression must leave the finding in New, got %+v", res.New)
	}
	if len(res.Suppressed) != 0 {
		t.Errorf("expired entries must not suppress, got %+v", res.Suppressed)
	}
	if len(res.Expired) != 2 {
		t.Errorf("both expired entries must be reported, got %+v", res.Expired)
	}
}

// TestEvaluateExpiryIsInclusiveOfTheDay: an entry is valid through the whole
// (UTC) day it names, and expired from the next midnight on. A date-granular
// field must not flip mid-day depending on the run's wall-clock hour.
func TestEvaluateExpiryIsInclusiveOfTheDay(t *testing.T) {
	current := []domain.Finding{
		gf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.31 exceeded threshold 0.20"),
	}
	known := []KnownIssue{{
		Category: "threshold", EvidenceRef: "error-rate", Reason: "r", Expires: "2026-06-11",
		ExpiresAt: time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC),
	}}

	lateOnExpiryDay := time.Date(2026, 6, 11, 23, 59, 0, 0, time.UTC)
	if res := Evaluate(nil, current, known, lateOnExpiryDay); len(res.Suppressed) != 1 || len(res.Expired) != 0 {
		t.Errorf("entry must still suppress on its expires day: %+v", res)
	}
	nextMidnight := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	if res := Evaluate(nil, current, known, nextMidnight); len(res.Suppressed) != 0 || len(res.Expired) != 1 {
		t.Errorf("entry must be expired from the next midnight: %+v", res)
	}
}

// TestEvaluateClassifiesAllBuckets drives the full four-way split in one call:
// new, resolved, persisting and suppressed.
func TestEvaluateClassifiesAllBuckets(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	baseline := []domain.Finding{
		gf(domain.FindingContract, domain.SeverityCritical, "orders", "2 contract violation(s) on orders"),
		gf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.31 exceeded threshold 0.20"),
	}
	current := []domain.Finding{
		gf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.44 exceeded threshold 0.20"), // persisting
		gf(domain.FindingAvailability, domain.SeverityCritical, "checkout", "6 consecutive failures on checkout"),    // new
		gf(domain.FindingContract, domain.SeverityCritical, "cart", "1 contract violation(s) on cart"),               // suppressed
	}
	known := []KnownIssue{{
		Category: "contract", EvidenceRef: "cart", Reason: "fix in review", Expires: "2026-07-01",
		ExpiresAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}}

	res := Evaluate(baseline, current, known, now)
	if len(res.New) != 1 || res.New[0].EvidenceRef != "checkout" {
		t.Errorf("New = %+v", res.New)
	}
	if len(res.Resolved) != 1 || res.Resolved[0].EvidenceRef != "orders" {
		t.Errorf("Resolved = %+v", res.Resolved)
	}
	if len(res.Persisting) != 1 || res.Persisting[0].EvidenceRef != "error-rate" {
		t.Errorf("Persisting = %+v", res.Persisting)
	}
	if len(res.Suppressed) != 1 || res.Suppressed[0].Finding.EvidenceRef != "cart" {
		t.Errorf("Suppressed = %+v", res.Suppressed)
	}
	if len(res.Expired) != 0 {
		t.Errorf("Expired = %+v, want none", res.Expired)
	}
}

// TestEvaluateSuppressionOnlyAffectsNew: a known issue matching a persisting
// finding changes nothing — persisting findings never fail the baseline gate,
// so reclassifying them would only hide honest state from the table.
func TestEvaluateSuppressionOnlyAffectsNew(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	shared := gf(domain.FindingThreshold, domain.SeverityWarning, "error-rate", "error rate 0.31 exceeded threshold 0.20")
	known := []KnownIssue{{
		Category: "threshold", EvidenceRef: "error-rate", Reason: "r", Expires: "2026-07-01",
		ExpiresAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}}

	res := Evaluate([]domain.Finding{shared}, []domain.Finding{shared}, known, now)
	if len(res.Persisting) != 1 || len(res.Suppressed) != 0 {
		t.Errorf("persisting finding must stay persisting even when a known issue matches: %+v", res)
	}
}
