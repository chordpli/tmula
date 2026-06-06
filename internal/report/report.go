package report

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/obs"
)

// Data is everything one run contributes to a report. It is built from domain
// and obs types only (never from api) so the api package can assemble it and
// call in without an import cycle.
type Data struct {
	Run      domain.RunExecution
	Stats    obs.Stats
	Findings []domain.Finding
	// Workers is the number of remote workers a distributed run fanned out to
	// (0 for a local run).
	Workers int
}

// HTML renders one run's report as a standalone HTML page: a header, a stats
// table and a findings list grouped by severity (critical first). Every dynamic
// value is escaped by html/template, so untrusted descriptions cannot inject
// markup.
func HTML(d Data) ([]byte, error) {
	var buf bytes.Buffer
	if err := reportTmpl.Execute(&buf, newReportView(d)); err != nil {
		return nil, fmt.Errorf("report: render html: %w", err)
	}
	return buf.Bytes(), nil
}

// CompareHTML renders a side-by-side comparison of two runs: per-metric deltas
// (with direction and percent change) and a findings diff. A finding present
// only in a is "resolved", only in b is "new", and in both is "persisting";
// findings are keyed by (category, evidenceRef, description). Output is a
// standalone HTML page with all dynamic values escaped.
func CompareHTML(a, b Data) ([]byte, error) {
	var buf bytes.Buffer
	if err := compareTmpl.Execute(&buf, newCompareView(a, b)); err != nil {
		return nil, fmt.Errorf("report: render compare html: %w", err)
	}
	return buf.Bytes(), nil
}

// --- view models ------------------------------------------------------------

// reportView is the flattened, presentation-ready shape the report template
// renders. Keeping formatting here (not in the template) keeps the template
// declarative and the rounding rules testable.
type reportView struct {
	Run          domain.RunExecution
	Workers      int
	Stats        statsRow
	StatusCodes  []statusCount
	FindingGroup []findingGroup
	HasFindings  bool
}

// statsRow is the formatted metric line for one run.
type statsRow struct {
	Total        int
	Errors       int
	Timeouts     int
	ErrorRatePct string // e.g. "2.00%"
	P50          string // milliseconds, one decimal
	P95          string
	P99          string
	Max          string
}

// statusCount is one HTTP status code and how often it was seen, sorted by code.
type statusCount struct {
	Code  int
	Count int
}

// findingGroup is the findings for one severity, in the order they should be
// displayed (critical first).
type findingGroup struct {
	Severity domain.Severity
	Label    string
	Class    string // css class controlling the accent color
	Findings []domain.Finding
}

func newReportView(d Data) reportView {
	return reportView{
		Run:          d.Run,
		Workers:      d.Workers,
		Stats:        newStatsRow(d.Stats),
		StatusCodes:  statusCounts(d.Stats.StatusCounts),
		FindingGroup: groupFindings(d.Findings),
		HasFindings:  len(d.Findings) > 0,
	}
}

func newStatsRow(s obs.Stats) statsRow {
	return statsRow{
		Total:        s.Total,
		Errors:       s.Errors,
		Timeouts:     s.Timeouts,
		ErrorRatePct: fmt.Sprintf("%.2f%%", s.ErrorRate*100),
		P50:          fmt.Sprintf("%.1f", s.P50),
		P95:          fmt.Sprintf("%.1f", s.P95),
		P99:          fmt.Sprintf("%.1f", s.P99),
		Max:          fmt.Sprintf("%.1f", s.Max),
	}
}

// statusCounts flattens the status-code map into a slice sorted by code so the
// rendered table is deterministic.
func statusCounts(m map[int]int) []statusCount {
	out := make([]statusCount, 0, len(m))
	for code, n := range m {
		out = append(out, statusCount{Code: code, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// severityOrder lists severities from most to least urgent; it drives both the
// display order of finding groups and the sort within a diff section.
var severityOrder = []domain.Severity{
	domain.SeverityCritical,
	domain.SeverityWarning,
	domain.SeverityInfo,
}

var severityMeta = map[domain.Severity]struct {
	label string
	class string
}{
	domain.SeverityCritical: {"Critical", "sev-critical"},
	domain.SeverityWarning:  {"Warning", "sev-warning"},
	domain.SeverityInfo:     {"Info", "sev-info"},
}

// groupFindings buckets findings by severity in critical-first order, dropping
// empty groups. Within a group the codebase's classifiers already emit findings
// in a stable order, which is preserved.
func groupFindings(fs []domain.Finding) []findingGroup {
	bySev := map[domain.Severity][]domain.Finding{}
	for _, f := range fs {
		bySev[f.Severity] = append(bySev[f.Severity], f)
	}
	var groups []findingGroup
	for _, sev := range severityOrder {
		group := bySev[sev]
		if len(group) == 0 {
			continue
		}
		meta := severityMeta[sev]
		groups = append(groups, findingGroup{
			Severity: sev,
			Label:    meta.label,
			Class:    meta.class,
			Findings: group,
		})
	}
	return groups
}
