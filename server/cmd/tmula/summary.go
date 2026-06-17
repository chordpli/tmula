package main

import (
	"fmt"
	"os"
	"strings"
)

// summaryPath resolves where the markdown run summary goes: an explicit
// --summary flag wins; otherwise GITHUB_STEP_SUMMARY (set by GitHub Actions for
// every step) makes the summary land on the workflow run page with zero
// configuration. Empty means no summary is written.
func summaryPath(flag string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv("GITHUB_STEP_SUMMARY")
}

// writeSummary appends the markdown to the file, creating it if needed. It
// appends rather than truncates because GITHUB_STEP_SUMMARY is one shared file
// per job: every step's contribution must survive.
func writeSummary(path, md string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("write summary %q: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(md); err != nil {
		return fmt.Errorf("write summary %q: %w", path, err)
	}
	return nil
}

// markdownReport renders the run report as GitHub-flavored markdown: the same
// information printReport prints to the terminal, shaped for a step summary or
// a PR comment.
func markdownReport(r cliReport) string {
	var b strings.Builder

	mode := r.Run.Mode
	if mode == "" {
		mode = "local"
	}
	if r.Workers > 0 {
		mode = fmt.Sprintf("%s, %d worker(s)", mode, r.Workers)
	}
	fmt.Fprintf(&b, "## tmula run `%s` — %s\n\n", r.Run.ID, r.Run.Status)
	if r.Run.KillReason != "" {
		fmt.Fprintf(&b, "> **Run %s:** %s\n\n", r.Run.Status, mdEscape(r.Run.KillReason))
	}

	fmt.Fprintf(&b, "| Requests | Errors | p50 (ms) | p95 (ms) | p99 (ms) | Max (ms) | Mode |\n")
	fmt.Fprintf(&b, "|---:|---:|---:|---:|---:|---:|:--|\n")
	fmt.Fprintf(&b, "| %d | %d (%.1f%%) | %.0f | %.0f | %.0f | %.0f | %s |\n\n",
		r.Stats.Total, r.Stats.Errors, r.Stats.ErrorRate*100,
		r.Stats.P50, r.Stats.P95, r.Stats.P99, r.Stats.Max, mdEscape(mode))
	if len(r.Stats.StatusCounts) > 0 {
		fmt.Fprintf(&b, "Status codes: `%s`\n\n", formatStatusCounts(r.Stats.StatusCounts))
	}

	// Non-failing run notes (e.g. an auth-expiry hint) render as block quotes so
	// they read as advisory remarks in the step summary, distinct from findings.
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "> Note: %s\n\n", mdEscape(n))
	}

	writeMetricsSection(&b, r)

	if len(r.Findings) == 0 {
		b.WriteString("No findings — the target handled this traffic cleanly.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "### Findings (%d)\n\n", len(r.Findings))
	b.WriteString("| Severity | Category | What broke | Where |\n")
	b.WriteString("|:--|:--|:--|:--|\n")
	for _, f := range r.Findings {
		where := ""
		if f.EvidenceRef != "" {
			// Backslash escapes do not work inside a code span, so a backtick in
			// the ref (impossible for sanitized node ids, but cheap to guard) is
			// replaced instead of escaped.
			where = "`" + strings.ReplaceAll(f.EvidenceRef, "`", "'") + "`"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			severityBadge(f.Severity), mdEscape(f.Category), mdEscape(f.Description), where)
	}
	return b.String()
}

// writeMetricsSection tabulates the run's server-side Prometheus correlation
// (when the run opted in) as min/last/max per series, plus the fetch problem
// when one occurred.
func writeMetricsSection(b *strings.Builder, r cliReport) {
	if len(r.ServerMetrics) == 0 && r.MetricsError == "" {
		return
	}
	b.WriteString("### Server metrics\n\n")
	if r.MetricsError != "" {
		fmt.Fprintf(b, "> Some series could not be fetched: %s\n\n", mdEscape(r.MetricsError))
	}
	if len(r.ServerMetrics) == 0 {
		return
	}
	b.WriteString("| Series | Samples | Min | Last | Max |\n|:--|---:|---:|---:|---:|\n")
	for _, s := range r.ServerMetrics {
		if len(s.Points) == 0 {
			fmt.Fprintf(b, "| %s | 0 | — | — | — |\n", mdEscape(s.Name))
			continue
		}
		minV, maxV := s.Points[0].V, s.Points[0].V
		for _, p := range s.Points[1:] {
			if p.V < minV {
				minV = p.V
			}
			if p.V > maxV {
				maxV = p.V
			}
		}
		fmt.Fprintf(b, "| %s | %d | %.4g | %.4g | %.4g |\n",
			mdEscape(s.Name), len(s.Points), minV, s.Points[len(s.Points)-1].V, maxV)
	}
	b.WriteString("\n")
}

// severityBadge renders a severity with a glanceable marker.
func severityBadge(sev string) string {
	switch strings.ToLower(sev) {
	case "critical":
		return "🔴 CRITICAL"
	case "warning":
		return "🟡 WARNING"
	default:
		return strings.ToUpper(sev)
	}
}

// mdEscape keeps free text from breaking out of a markdown table cell: pipes
// would split the row, a backtick could open a code span, "<" could start raw
// HTML, and newlines would end the row.
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "<", `\<`)
	return strings.ReplaceAll(s, "\n", " ")
}
