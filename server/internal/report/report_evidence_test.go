package report

import (
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
)

// evidenceData builds a report Data with one finding carrying a full evidence
// bundle and one without any, so the rendering tests cover both shapes.
func evidenceData() Data {
	ts := time.Date(2026, 6, 11, 10, 30, 0, 0, time.UTC)
	return Data{
		Run:   domain.RunExecution{ID: "run-1", Status: domain.RunCompleted, StartedAt: ts},
		Stats: obs.Stats{Total: 10, Errors: 3},
		Findings: []domain.Finding{
			{
				RunID: "run-1", Category: domain.FindingContract, Severity: domain.SeverityCritical,
				EvidenceRef: "api-checkout", Description: "3 contract violation(s) on api-checkout", Count: 3,
				Evidence: &domain.FindingEvidence{
					Sessions: []domain.EvidenceSession{{
						SessionID:  "vu-browser-s42",
						Seed:       42,
						UserIndex:  41,
						Persona:    "browser",
						Path:       []domain.ID{"home", "search", "checkout"},
						StatusCode: 503,
						LatencyMs:  812.5,
						ErrorClass: "transport",
						TS:         ts,
					}},
					TimeBuckets: []domain.EvidenceBucket{
						{Label: "0-25%", Count: 0},
						{Label: "25-50%", Count: 1},
						{Label: "50-75%", Count: 0},
						{Label: "75-100%", Count: 2},
					},
					StatusCounts: map[int]int{503: 2, 500: 1},
				},
			},
			{
				RunID: "run-1", Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
				EvidenceRef: "error-rate", Description: "error rate 0.30 exceeded threshold 0.20",
			},
		},
	}
}

// TestReportHTMLRendersEvidence: a finding with an evidence bundle renders a
// collapsible section listing the representative sessions (with the
// X-Tmula-Session-ID log-correlation hint and reproduce coordinates), the
// walked path, the status distribution and the time distribution.
func TestReportHTMLRendersEvidence(t *testing.T) {
	out, err := HTML(evidenceData())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)

	for _, want := range []string{
		"<details",                 // collapsible evidence section
		"X-Tmula-Session-ID",       // how to grep server logs for the session
		"vu-browser-s42",           // the representative session id
		"home → search → checkout", // the walked path
		"browser",                  // persona
		"812.5",                    // failing request latency
		"transport",                // failing request error class
		"503",                      // status distribution
		"75-100%",                  // time distribution bucket label
	} {
		if !strings.Contains(html, want) {
			t.Errorf("report html missing %q", want)
		}
	}
	// Reproduce coordinates are visible.
	if !strings.Contains(html, "42") || !strings.Contains(html, "41") {
		t.Error("report html missing the seed / user-index reproduce coordinates")
	}
}

// TestReportHTMLWithoutEvidenceOmitsSection: a finding without a bundle (a
// legacy persisted finding, or a summary-derived coarse one) renders exactly
// one details section for the run — the one belonging to the evidence-bearing
// finding — never an empty shell.
func TestReportHTMLWithoutEvidenceOmitsSection(t *testing.T) {
	d := evidenceData()
	out, err := HTML(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if n := strings.Count(string(out), "<details"); n != 1 {
		t.Errorf("details sections = %d, want 1 (only the evidence-bearing finding)", n)
	}

	d.Findings = d.Findings[1:] // only the evidence-less finding remains
	out, err = HTML(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(out), "<details") {
		t.Error("evidence section rendered for a finding without evidence")
	}
}

// TestReportHTMLEscapesEvidence: evidence strings are untrusted (session ids
// embed user-supplied base ids; error classes derive from errors), so they
// must be HTML-escaped like every other dynamic value.
func TestReportHTMLEscapesEvidence(t *testing.T) {
	d := evidenceData()
	d.Findings[0].Evidence.Sessions[0].SessionID = `<script>alert(1)</script>`
	out, err := HTML(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(out), "<script>alert(1)</script>") {
		t.Error("evidence session id rendered unescaped")
	}
}
