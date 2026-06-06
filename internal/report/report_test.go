package report

import (
	"html"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/obs"
)

// htmlUnescape turns rendered entities (e.g. &#43;) back into their characters
// so a test can assert on the text a browser would display.
func htmlUnescape(s string) string { return html.UnescapeString(s) }

func sampleData() Data {
	return Data{
		Run: domain.RunExecution{
			ID: "run-7", ExperimentID: "exp-1", Mode: domain.RunDistributed,
			Status: domain.RunCompleted, StartedAt: time.Unix(1700000000, 0),
		},
		Stats: obs.Stats{
			Total: 100, Errors: 2, Timeouts: 1, ErrorRate: 0.02,
			StatusCounts: map[int]int{200: 98, 500: 2},
			P50:          10, P95: 12, P99: 20, Max: 33,
		},
		Findings: []domain.Finding{
			{RunID: "run-7", Category: domain.FindingContract, Severity: domain.SeverityCritical,
				EvidenceRef: "node-a", Description: "2 contract violation(s) on node-a"},
		},
		Workers: 3,
	}
}

func TestHTMLContainsKeyValues(t *testing.T) {
	out, err := HTML(sampleData())
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	html := string(out)

	for _, want := range []string{
		"run-7",       // run id in header
		"exp-1",       // experiment id
		"distributed", // mode
		"2.00%",       // error rate formatted
		"12.0",        // p95
		"33.0",        // max
		"500",         // status code present
		"node-a",      // finding evidence ref
		"contract",    // finding category
		"Critical",    // severity group label
		"<style>",     // self-contained: inline style block
		"<!doctype html>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}

	// Self-contained: no external asset references.
	for _, banned := range []string{"<link", "<script", "src=", "href="} {
		if strings.Contains(html, banned) {
			t.Errorf("HTML should be self-contained but contains %q", banned)
		}
	}
}

func TestHTMLEscapesScriptInDescription(t *testing.T) {
	d := sampleData()
	d.Findings = []domain.Finding{{
		RunID: "run-7", Category: domain.FindingContract, Severity: domain.SeverityCritical,
		EvidenceRef: "<b>x</b>", Description: `<script>alert('xss')</script>`,
	}}
	out, err := HTML(d)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	html := string(out)

	// The raw script tag from the finding must never appear verbatim.
	if strings.Contains(html, "<script>alert") {
		t.Fatal("description was not escaped: raw <script> present")
	}
	// The escaped form must be present instead.
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("expected escaped <script> entity in output")
	}
	if !strings.Contains(html, "&lt;b&gt;x&lt;/b&gt;") {
		t.Error("expected escaped evidence ref in output")
	}
}

func TestHTMLCleanRun(t *testing.T) {
	d := sampleData()
	d.Findings = nil
	out, err := HTML(d)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if !strings.Contains(string(out), "clean run") {
		t.Error("expected a clean-run message when there are no findings")
	}
}

func TestCompareHTMLClassifiesFindings(t *testing.T) {
	shared := domain.Finding{Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
		EvidenceRef: "", Description: "error rate 0.05 exceeded threshold 0.02"}
	onlyA := domain.Finding{Category: domain.FindingContract, Severity: domain.SeverityCritical,
		EvidenceRef: "node-a", Description: "2 contract violation(s) on node-a"}
	onlyB := domain.Finding{Category: domain.FindingAvailability, Severity: domain.SeverityCritical,
		EvidenceRef: "node-b", Description: "6 consecutive failures on node-b"}

	a := sampleData()
	a.Run.ID = "run-a"
	a.Stats.P95 = 12
	a.Findings = []domain.Finding{shared, onlyA}

	b := sampleData()
	b.Run.ID = "run-b"
	b.Stats.P95 = 18
	b.Findings = []domain.Finding{shared, onlyB}

	out, err := CompareHTML(a, b)
	if err != nil {
		t.Fatalf("CompareHTML: %v", err)
	}
	html := string(out)

	// A finding present only in B is new/regressed; only in A is resolved.
	newIdx := strings.Index(html, "regressed or introduced in B")
	resolvedIdx := strings.Index(html, "present in A, gone in B")
	persistIdx := strings.Index(html, "present in both")
	if newIdx < 0 || resolvedIdx < 0 || persistIdx < 0 {
		t.Fatal("compare output is missing a diff section header")
	}

	// onlyB must appear in the new section (before the resolved section starts).
	if !between(html, "node-b", newIdx, resolvedIdx) {
		t.Error("B-only finding not classified as new")
	}
	// onlyA must appear in the resolved section (between resolved and persisting).
	if !between(html, "node-a", resolvedIdx, persistIdx) {
		t.Error("A-only finding not classified as resolved")
	}
	// shared must appear in the persisting section.
	if !between(html, "error rate 0.05", persistIdx, len(html)) {
		t.Error("shared finding not classified as persisting")
	}

	// p95 delta 12 -> 18 is a +50.0% regression. html/template numeric-escapes
	// the leading '+' (to &#43;), so compare against the rendered text.
	if rendered := htmlUnescape(html); !strings.Contains(rendered, "+50.0%") {
		t.Error("expected p95 delta of +50.0% in comparison")
	}
	if !strings.Contains(html, "run-a") || !strings.Contains(html, "run-b") {
		t.Error("expected both run ids in comparison header")
	}
}

func TestCompareHTMLEscapesAndZeroBaseline(t *testing.T) {
	a := sampleData()
	a.Stats.ErrorRate = 0 // zero baseline -> "new", not an infinity
	b := sampleData()
	b.Stats.ErrorRate = 0.24
	b.Findings = []domain.Finding{{
		Category: domain.FindingContract, Severity: domain.SeverityCritical,
		Description: `<img src=x onerror=alert(1)>`,
	}}

	out, err := CompareHTML(a, b)
	if err != nil {
		t.Fatalf("CompareHTML: %v", err)
	}
	html := string(out)

	if strings.Contains(html, "<img src=x") {
		t.Error("malicious description was not escaped in comparison output")
	}
	if !strings.Contains(html, "new") {
		t.Error("expected zero-baseline error rate to render as 'new'")
	}
}

// between reports whether sub occurs in s within the byte range [lo, hi).
func between(s, sub string, lo, hi int) bool {
	if lo < 0 || hi > len(s) || lo >= hi {
		return false
	}
	idx := strings.Index(s[lo:hi], sub)
	return idx >= 0
}
