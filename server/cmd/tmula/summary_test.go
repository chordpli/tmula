package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleReport() cliReport {
	var r cliReport
	r.Run.ID = "run-7"
	r.Run.Status = "completed"
	r.Run.Mode = "local"
	r.Stats.Total = 414
	r.Stats.Errors = 6
	r.Stats.ErrorRate = 0.0145
	r.Stats.P50, r.Stats.P95, r.Stats.P99, r.Stats.Max = 9, 25, 46, 81
	r.Stats.StatusCounts = map[string]int{"200": 408, "500": 6}
	r.Findings = []cliFinding{
		{Category: "contract", Severity: "critical",
			Description: "3 contract violation(s) on post_cart (unexpected error on the happy path)",
			EvidenceRef: "post_cart"},
		{Category: "threshold", Severity: "warning",
			Description: "error rate 0.31 exceeded threshold 0.20 | spiked `fast` <hot>"},
	}
	return r
}

func TestMarkdownReportRendersRunAndFindings(t *testing.T) {
	md := markdownReport(sampleReport())

	for _, want := range []string{
		"run-7",
		"completed",
		"414",         // request count
		"1.5%",        // error rate, one decimal
		"| 25 | 46 |", // p95 / p99 cells
		"`200:408 500:6`",
		"CRITICAL",
		"contract",
		"post_cart",
		"WARNING",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("summary missing %q:\n%s", want, md)
		}
	}
	// Markdown-active characters inside a finding description must not break
	// out of the table cell: pipes split rows, backticks open code spans, and
	// "<" starts raw HTML.
	if !strings.Contains(md, "threshold 0.20 \\| spiked \\`fast\\` \\<hot>") {
		t.Errorf("description not escaped for a table cell:\n%s", md)
	}
}

func TestMarkdownReportCleanRun(t *testing.T) {
	r := sampleReport()
	r.Findings = nil
	md := markdownReport(r)
	if !strings.Contains(md, "No findings") {
		t.Errorf("clean run should say so:\n%s", md)
	}
	if strings.Contains(md, "| Severity |") {
		t.Errorf("clean run should not render an empty findings table:\n%s", md)
	}
}

func TestMarkdownReportKilledRunShowsReason(t *testing.T) {
	r := sampleReport()
	r.Run.Status = "killed"
	r.Run.KillReason = "auto: rolling error rate 0.9 over last 50 exceeded threshold 0.5"
	md := markdownReport(r)
	if !strings.Contains(md, "killed") || !strings.Contains(md, "rolling error rate") {
		t.Errorf("killed run should surface its reason:\n%s", md)
	}
}

func TestMarkdownReportTabulatesServerMetrics(t *testing.T) {
	r := sampleReport()
	r.ServerMetrics = []cliMetricSeries{{Name: "db conns"}}
	r.ServerMetrics[0].Points = []struct {
		TS int64   `json:"ts"`
		V  float64 `json:"v"`
	}{{TS: 1, V: 12}, {TS: 2, V: 3}, {TS: 3, V: 7}}
	r.MetricsError = "broken: prometheus: unknown query"
	md := markdownReport(r)
	if !strings.Contains(md, "### Server metrics") {
		t.Fatalf("metrics section missing:\n%s", md)
	}
	if !strings.Contains(md, "| db conns | 3 | 3 | 7 | 12 |") {
		t.Errorf("metrics row min/last/max wrong:\n%s", md)
	}
	if !strings.Contains(md, "unknown query") {
		t.Errorf("fetch error note missing:\n%s", md)
	}

	// Without the opt-in, no section appears.
	if md := markdownReport(sampleReport()); strings.Contains(md, "Server metrics") {
		t.Errorf("metrics section should be absent when not opted in:\n%s", md)
	}
}

func TestWriteSummaryAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(path, []byte("earlier step\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSummary(path, "## tmula\n"); err != nil {
		t.Fatalf("writeSummary: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// GITHUB_STEP_SUMMARY is shared by every step in a job, so the writer must
	// append, never truncate.
	if got := string(data); !strings.HasPrefix(got, "earlier step\n") || !strings.Contains(got, "## tmula") {
		t.Errorf("summary should append after existing content, got:\n%s", got)
	}
}

func TestSummaryPathPrefersFlagOverEnv(t *testing.T) {
	t.Setenv("GITHUB_STEP_SUMMARY", "/env/summary.md")
	if got := summaryPath("/flag/summary.md"); got != "/flag/summary.md" {
		t.Errorf("summaryPath(flag) = %q, want the explicit flag", got)
	}
	if got := summaryPath(""); got != "/env/summary.md" {
		t.Errorf("summaryPath('') = %q, want the env fallback", got)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	if got := summaryPath(""); got != "" {
		t.Errorf("summaryPath with no flag and no env = %q, want empty (disabled)", got)
	}
}
