package report

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
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
	// ServerMetrics carries the Prometheus series fetched over the run's window
	// when the run opted in; MetricsError notes a fetch problem. Both are
	// optional report extras.
	ServerMetrics []domain.MetricSeries
	MetricsError  string
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
// findings are keyed by (category, evidenceRef) so run-specific numbers in
// the description do not affect identity. Output is a standalone HTML page
// with all dynamic values escaped.
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
	Run           domain.RunExecution
	Workers       int
	Stats         statsRow
	StatusCodes   []statusCount
	FindingGroup  []findingGroup
	HasFindings   bool
	ServerMetrics []metricRow
	MetricsError  string
}

// metricRow is one server-side series formatted for the report table.
type metricRow struct {
	Name    string
	Samples int
	Min     string
	Last    string
	Max     string
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
	Findings []findingRow
}

// findingRow is one finding flattened for display: the identity fields the
// template always shows, plus the formatted evidence bundle when the finding
// carries one (nil renders no evidence section at all).
type findingRow struct {
	Category    domain.FindingCategory
	EvidenceRef string
	Description string
	Evidence    *evidenceView
}

// evidenceView is a finding's evidence bundle formatted for the template:
// representative sessions as table rows, plus the status-code and run-window
// timing distributions.
type evidenceView struct {
	Sessions     []evidenceSessionRow
	StatusCounts []statusCount
	TimeBuckets  []domain.EvidenceBucket
}

// evidenceSessionRow is one representative session formatted for display. ID
// is the value to grep server logs for (the X-Tmula-Session-ID header); Seed
// and UserIndex are the reproduce coordinates; Path is the pre-joined journey
// ("" when the producing path ships no journeys, e.g. the distributed stream).
type evidenceSessionRow struct {
	ID         string
	Seed       int64
	UserIndex  int64
	Persona    string
	Path       string
	StatusCode int
	Latency    string // milliseconds, one decimal (matches statsRow)
	ErrorClass string
	TS         time.Time
}

func newReportView(d Data) reportView {
	return reportView{
		Run:           d.Run,
		Workers:       d.Workers,
		Stats:         newStatsRow(d.Stats),
		StatusCodes:   statusCounts(d.Stats.StatusCounts),
		FindingGroup:  groupFindings(d.Findings),
		HasFindings:   len(d.Findings) > 0,
		ServerMetrics: metricRows(d.ServerMetrics),
		MetricsError:  d.MetricsError,
	}
}

// metricRows summarizes each fetched series as min/last/max over its samples;
// a series without points is shown with dashes rather than dropped, so the
// operator sees the query returned nothing.
func metricRows(series []domain.MetricSeries) []metricRow {
	rows := make([]metricRow, 0, len(series))
	for _, s := range series {
		row := metricRow{Name: s.Name, Samples: len(s.Points), Min: "—", Last: "—", Max: "—"}
		if len(s.Points) > 0 {
			minV, maxV := s.Points[0].V, s.Points[0].V
			for _, p := range s.Points[1:] {
				if p.V < minV {
					minV = p.V
				}
				if p.V > maxV {
					maxV = p.V
				}
			}
			row.Min = fmt.Sprintf("%.4g", minV)
			row.Last = fmt.Sprintf("%.4g", s.Points[len(s.Points)-1].V)
			row.Max = fmt.Sprintf("%.4g", maxV)
		}
		rows = append(rows, row)
	}
	return rows
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
	bySev := map[domain.Severity][]findingRow{}
	for _, f := range fs {
		bySev[f.Severity] = append(bySev[f.Severity], newFindingRow(f))
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

// newFindingRow flattens one finding for display, formatting its evidence
// bundle when present.
func newFindingRow(f domain.Finding) findingRow {
	return findingRow{
		Category:    f.Category,
		EvidenceRef: f.EvidenceRef,
		Description: f.Description,
		Evidence:    newEvidenceView(f.Evidence),
	}
}

// newEvidenceView formats a finding's evidence bundle for the template; nil in,
// nil out, so legacy findings render exactly as before.
func newEvidenceView(ev *domain.FindingEvidence) *evidenceView {
	if ev == nil {
		return nil
	}
	v := &evidenceView{
		StatusCounts: statusCounts(ev.StatusCounts),
		TimeBuckets:  ev.TimeBuckets,
	}
	for _, s := range ev.Sessions {
		v.Sessions = append(v.Sessions, evidenceSessionRow{
			ID:         s.SessionID,
			Seed:       s.Seed,
			UserIndex:  s.UserIndex,
			Persona:    s.Persona,
			Path:       joinPath(s.Path),
			StatusCode: s.StatusCode,
			Latency:    fmt.Sprintf("%.1f", s.LatencyMs),
			ErrorClass: s.ErrorClass,
			TS:         s.TS,
		})
	}
	return v
}

// joinPath renders a session's node journey as a single arrow-joined string,
// so the template treats the path as one (escaped) value.
func joinPath(path []domain.ID) string {
	if len(path) == 0 {
		return ""
	}
	parts := make([]string, len(path))
	for i, id := range path {
		parts[i] = string(id)
	}
	return strings.Join(parts, " → ")
}
