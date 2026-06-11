package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/gate"
)

// errNewFindings signals the baseline regression gate: the run introduced
// findings that the baseline run did not have. main maps it to exit 3 — like
// errFindings it is an expected gate outcome, not a crash, but it gets its own
// code so CI can tell "known problems persist" (0 with a baseline) from "this
// change broke something new" (3).
var errNewFindings = errors.New("new findings vs baseline")

// loadBaselineFile reads a baseline report from a JSON file — the output of a
// previous `tmula run --json`, the artifact a CI job saves from its main-branch
// run. It returns the baseline findings and a label for output (the baseline
// run's id when the file carries one, else the path).
func loadBaselineFile(path string) ([]cliFinding, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read baseline report: %w", err)
	}
	var rep cliReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, "", fmt.Errorf("parse baseline report %q: %w", path, err)
	}
	label := rep.Run.ID
	if label == "" {
		label = path
	}
	return rep.Findings, label, nil
}

// fetchBaselineRun resolves a baseline run id through the engine's report
// endpoint — the CLI's one existing path to persisted runs (the engine fronts
// the Store, and rebuilds finalized reports from it after eviction).
func fetchBaselineRun(ctx context.Context, engineBase, runID string) ([]cliFinding, string, error) {
	var rep cliReport
	if err := getJSON(ctx, engineBase+"/api/runs/"+runID+"/report", &rep); err != nil {
		return nil, "", fmt.Errorf("fetch baseline run %q: %w", runID, err)
	}
	return rep.Findings, runID, nil
}

// loadKnownIssues reads and validates the --known-issues YAML file.
func loadKnownIssues(path string) ([]gate.KnownIssue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read known issues: %w", err)
	}
	issues, err := gate.ParseKnownIssues(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return issues, nil
}

// toDomainFindings converts the CLI's JSON-mirror findings into domain findings
// so the gate can reuse the report package's (category, evidenceRef) diff.
func toDomainFindings(fs []cliFinding) []domain.Finding {
	out := make([]domain.Finding, len(fs))
	for i, f := range fs {
		out[i] = domain.Finding{
			Category:    domain.FindingCategory(f.Category),
			Severity:    domain.Severity(f.Severity),
			Description: f.Description,
			EvidenceRef: f.EvidenceRef,
		}
	}
	return out
}

// warnExpired reports expired known issues on stderr — stderr so the warning
// survives --json mode without corrupting the report document. An expired
// entry no longer suppresses (its finding reddens the gate again); the warning
// tells the operator to re-triage or delete it.
func warnExpired(expired []gate.KnownIssue) {
	for _, ki := range expired {
		fmt.Fprintf(os.Stderr, "warning: known issue expired %s: %s/%s (%s) no longer suppresses\n",
			ki.Expires, ki.Category, ki.EvidenceRef, ki.Reason)
	}
}

// gateCounts is the one-line bucket tally shared by the terminal and markdown
// renderings.
func gateCounts(res gate.Result) string {
	return fmt.Sprintf("%d new · %d resolved · %d persisting · %d suppressed",
		len(res.New), len(res.Resolved), len(res.Persisting), len(res.Suppressed))
}

// evidenceSuffix renders a finding's evidence ref the way printReport does.
func evidenceSuffix(ref string) string {
	if ref == "" {
		return ""
	}
	return " [" + ref + "]"
}

// printGateResult renders the baseline gate verdict for the terminal: the
// bucket tally, then the entries that need eyes — new findings (they fail the
// gate) and suppressed ones (so a silenced problem is never invisible).
func printGateResult(res gate.Result, label string) {
	fmt.Printf("\nBaseline gate vs %s: %s\n", label, gateCounts(res))
	for _, f := range res.New {
		fmt.Printf("  new        • [%s] %s: %s%s\n",
			strings.ToUpper(string(f.Severity)), f.Category, f.Description, evidenceSuffix(f.EvidenceRef))
	}
	for _, s := range res.Suppressed {
		fmt.Printf("  suppressed • [%s] %s: %s%s (known issue: %s, expires %s)\n",
			strings.ToUpper(string(s.Finding.Severity)), s.Finding.Category, s.Finding.Description,
			evidenceSuffix(s.Finding.EvidenceRef), s.Issue.Reason, s.Issue.Expires)
	}
}

// markdownBaselineGate renders the baseline gate verdict as a markdown section
// for the step summary: the four buckets in one table (new first — they are
// what failed the job), expired suppressions called out above it.
func markdownBaselineGate(res gate.Result, label string) string {
	var b strings.Builder
	// Backslash escapes do not work inside a code span, so a backtick in the
	// label is replaced rather than escaped (same guard as the findings table).
	fmt.Fprintf(&b, "### Baseline gate — vs `%s`\n\n", strings.ReplaceAll(label, "`", "'"))
	fmt.Fprintf(&b, "%s\n\n", gateCounts(res))

	if len(res.Expired) > 0 {
		fmt.Fprintf(&b, "> ⚠️ %d expired known issue(s) no longer suppress — re-triage or delete them:\n", len(res.Expired))
		for _, ki := range res.Expired {
			fmt.Fprintf(&b, "> - %s/%s expired %s (%s)\n",
				mdEscape(ki.Category), mdEscape(ki.EvidenceRef), mdEscape(ki.Expires), mdEscape(ki.Reason))
		}
		b.WriteString("\n")
	}

	if len(res.New)+len(res.Resolved)+len(res.Persisting)+len(res.Suppressed) == 0 {
		b.WriteString("No findings in either run.\n")
		return b.String()
	}

	b.WriteString("| Status | Severity | Category | What broke | Where | Note |\n")
	b.WriteString("|:--|:--|:--|:--|:--|:--|\n")
	for _, f := range res.New {
		gateRow(&b, "🆕 new", f, "")
	}
	for _, s := range res.Suppressed {
		gateRow(&b, "🔕 suppressed", s.Finding,
			fmt.Sprintf("known issue: %s (expires %s)", mdEscape(s.Issue.Reason), mdEscape(s.Issue.Expires)))
	}
	for _, f := range res.Persisting {
		gateRow(&b, "⏳ persisting", f, "")
	}
	for _, f := range res.Resolved {
		gateRow(&b, "✅ resolved", f, "")
	}
	return b.String()
}

// gateRow writes one finding row of the baseline gate table. note is already
// markdown-escaped by the caller (it may carry formatting of its own).
func gateRow(b *strings.Builder, status string, f domain.Finding, note string) {
	where := ""
	if f.EvidenceRef != "" {
		where = "`" + strings.ReplaceAll(f.EvidenceRef, "`", "'") + "`"
	}
	fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s |\n",
		status, severityBadge(string(f.Severity)), mdEscape(string(f.Category)),
		mdEscape(f.Description), where, note)
}
