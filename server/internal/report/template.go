package report

import (
	"html/template"
	"time"
)

// baseStyle is the inline stylesheet shared by every report page. It is kept
// here as a constant so both templates stay self-contained: no external CSS,
// no fonts, no JS — the page renders identically offline or attached to a mail.
const baseStyle = `
:root { color-scheme: light dark; }
* { box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  margin: 0; padding: 2rem; line-height: 1.5; color: #1b1f24; background: #f6f8fa;
}
main { max-width: 960px; margin: 0 auto; }
h1 { font-size: 1.5rem; margin: 0 0 0.25rem; }
h2 { font-size: 1.1rem; margin: 2rem 0 0.75rem; padding-bottom: 0.3rem; border-bottom: 1px solid #d0d7de; }
.meta { color: #57606a; font-size: 0.9rem; margin: 0 0 0.5rem; }
.meta code { background: #eaeef2; padding: 0.1rem 0.35rem; border-radius: 4px; }
table { border-collapse: collapse; width: 100%; background: #fff; border: 1px solid #d0d7de; border-radius: 6px; overflow: hidden; }
th, td { text-align: left; padding: 0.5rem 0.75rem; border-bottom: 1px solid #eaeef2; }
th { background: #f6f8fa; font-weight: 600; }
tr:last-child td { border-bottom: none; }
td.num, th.num { text-align: right; font-variant-numeric: tabular-nums; }
.pill { display: inline-block; padding: 0.1rem 0.55rem; border-radius: 999px; font-size: 0.8rem; font-weight: 600; }
.status-completed { background: #dafbe1; color: #1a7f37; }
.status-running   { background: #ddf4ff; color: #0969da; }
.status-killed,
.status-failed    { background: #ffebe9; color: #cf222e; }
.findings { list-style: none; padding: 0; margin: 0; }
.finding { background: #fff; border: 1px solid #d0d7de; border-left-width: 4px; border-radius: 6px; padding: 0.6rem 0.8rem; margin-bottom: 0.6rem; }
.finding .cat { font-weight: 600; }
.finding .ref { color: #57606a; font-size: 0.85rem; }
.finding.sev-critical { border-left-color: #cf222e; }
.finding.sev-warning  { border-left-color: #bf8700; }
.finding.sev-info     { border-left-color: #0969da; }
.evidence { margin-top: 0.5rem; font-size: 0.85rem; }
.evidence summary { cursor: pointer; color: #57606a; font-weight: 600; }
.evidence table { margin: 0.4rem 0 0.6rem; font-size: 0.85rem; }
.evidence .hint { color: #57606a; font-size: 0.8rem; margin: 0.5rem 0 0.25rem; }
.evidence code { background: #eaeef2; padding: 0.05rem 0.3rem; border-radius: 4px; }
.group-label { font-size: 0.95rem; font-weight: 600; margin: 1rem 0 0.5rem; }
.group-label.sev-critical { color: #cf222e; }
.group-label.sev-warning  { color: #bf8700; }
.group-label.sev-info     { color: #0969da; }
.empty { color: #57606a; font-style: italic; }
.cols { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; }
.dir-up   { color: #cf222e; font-weight: 600; }
.dir-down { color: #1a7f37; font-weight: 600; }
.dir-flat { color: #57606a; }
.diff-new      .finding { border-left-color: #cf222e; }
.diff-resolved .finding { border-left-color: #1a7f37; }
.diff-section h3 { font-size: 1rem; margin: 1.25rem 0 0.5rem; }
.tag { display: inline-block; padding: 0.1rem 0.5rem; border-radius: 4px; font-size: 0.75rem; font-weight: 600; }
.tag-new      { background: #ffebe9; color: #cf222e; }
.tag-resolved { background: #dafbe1; color: #1a7f37; }
.tag-persist  { background: #eaeef2; color: #57606a; }
footer { margin-top: 2.5rem; color: #8c959f; font-size: 0.8rem; text-align: center; }
`

// tmplFuncs are the formatting helpers the templates use. Time formatting lives
// here so the templates stay free of layout-string literals.
var tmplFuncs = template.FuncMap{
	"fmtTime": fmtTime,
	"fmtPtr":  fmtTimePtr,
}

// fmtTime renders a timestamp in RFC3339, or a dash when zero (an unset
// first-seen on synthetic findings).
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

// fmtTimePtr renders an optional timestamp (e.g. a run's EndedAt), showing a
// dash when the run has not finished.
func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return fmtTime(*t)
}

// reportTmpl renders a single run. compareTmpl renders two side by side. Both
// embed baseStyle and are parsed once at init; a parse error is a programmer
// error in the literal, so Must is appropriate.
var (
	reportTmpl  = template.Must(template.New("report").Funcs(tmplFuncs).Parse(reportHTML))
	compareTmpl = template.Must(template.New("compare").Funcs(tmplFuncs).Parse(compareHTML))
)

const reportHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Run {{.Run.ID}} report</title>
<style>` + baseStyle + `</style>
</head>
<body>
<main>
  <h1>Run {{.Run.ID}}</h1>
  <p class="meta">
    <span class="pill status-{{.Run.Status}}">{{.Run.Status}}</span>
    mode <code>{{.Run.Mode}}</code> · workers <code>{{.Workers}}</code>
  </p>
  <p class="meta">experiment <code>{{.Run.ExperimentID}}</code> · started {{fmtTime .Run.StartedAt}} · ended {{fmtPtr .Run.EndedAt}}</p>
  {{if .Run.KillReason}}<p class="meta">kill reason: {{.Run.KillReason}}</p>{{end}}

  <h2>Stats</h2>
  <table>
    <tbody>
      <tr><th>Total requests</th><td class="num">{{.Stats.Total}}</td></tr>
      <tr><th>Errors</th><td class="num">{{.Stats.Errors}}</td></tr>
      <tr><th>Timeouts</th><td class="num">{{.Stats.Timeouts}}</td></tr>
      <tr><th>Error rate</th><td class="num">{{.Stats.ErrorRatePct}}</td></tr>
      <tr><th>p50</th><td class="num">{{.Stats.P50}} ms</td></tr>
      <tr><th>p95</th><td class="num">{{.Stats.P95}} ms</td></tr>
      <tr><th>p99</th><td class="num">{{.Stats.P99}} ms</td></tr>
      <tr><th>max</th><td class="num">{{.Stats.Max}} ms</td></tr>
    </tbody>
  </table>

  <h2>Status codes</h2>
  {{if .StatusCodes}}
  <table>
    <thead><tr><th>Code</th><th class="num">Count</th></tr></thead>
    <tbody>
      {{range .StatusCodes}}<tr><td>{{.Code}}</td><td class="num">{{.Count}}</td></tr>{{end}}
    </tbody>
  </table>
  {{else}}<p class="empty">No responses recorded.</p>{{end}}

  {{if or .ServerMetrics .MetricsError}}
  <h2>Server metrics</h2>
  {{if .MetricsError}}<p class="meta">some series could not be fetched: {{.MetricsError}}</p>{{end}}
  {{if .ServerMetrics}}
  <table>
    <thead><tr><th>Series</th><th class="num">Samples</th><th class="num">Min</th><th class="num">Last</th><th class="num">Max</th></tr></thead>
    <tbody>
      {{range .ServerMetrics}}<tr><td>{{.Name}}</td><td class="num">{{.Samples}}</td><td class="num">{{.Min}}</td><td class="num">{{.Last}}</td><td class="num">{{.Max}}</td></tr>{{end}}
    </tbody>
  </table>
  {{end}}
  {{end}}

  <h2>Findings</h2>
  {{if .HasFindings}}
    {{range .FindingGroup}}
      {{$class := .Class}}
      <p class="group-label {{.Class}}">{{.Label}} ({{len .Findings}})</p>
      <ul class="findings">
        {{range .Findings}}
        <li class="finding {{$class}}">
          <span class="cat">{{.Category}}</span>
          {{if .EvidenceRef}}<span class="ref">· {{.EvidenceRef}}</span>{{end}}
          <div>{{.Description}}</div>
          {{if .Evidence}}
          <details class="evidence">
            <summary>Evidence{{with .Evidence.Sessions}} · {{len .}} representative session(s){{end}}</summary>
            {{if .Evidence.Sessions}}
            <p class="hint">Correlate with the target's logs by grepping for the <code>X-Tmula-Session-ID</code> header value below; reproduce a session from its seed and user index.</p>
            <table>
              <thead><tr><th>Session</th><th>Persona</th><th class="num">Seed</th><th class="num">User #</th><th>Path to failure</th><th class="num">Status</th><th class="num">Latency</th><th>Error</th><th>At</th></tr></thead>
              <tbody>
                {{range .Evidence.Sessions}}<tr><td><code>{{.ID}}</code></td><td>{{if .Persona}}{{.Persona}}{{else}}—{{end}}</td><td class="num">{{.Seed}}</td><td class="num">{{.UserIndex}}</td><td>{{if .Path}}{{.Path}}{{else}}—{{end}}</td><td class="num">{{if .StatusCode}}{{.StatusCode}}{{else}}—{{end}}</td><td class="num">{{.Latency}} ms</td><td>{{if .ErrorClass}}{{.ErrorClass}}{{else}}—{{end}}</td><td>{{fmtTime .TS}}</td></tr>{{end}}
              </tbody>
            </table>
            {{end}}
            {{if .Evidence.StatusCounts}}
            <p class="hint">Status codes across all occurrences</p>
            <table>
              <thead><tr><th>Code</th><th class="num">Count</th></tr></thead>
              <tbody>
                {{range .Evidence.StatusCounts}}<tr><td>{{.Code}}</td><td class="num">{{.Count}}</td></tr>{{end}}
              </tbody>
            </table>
            {{end}}
            {{if .Evidence.TimeBuckets}}
            <p class="hint">When in the run the occurrences landed</p>
            <table>
              <thead><tr><th>Run window</th><th class="num">Count</th></tr></thead>
              <tbody>
                {{range .Evidence.TimeBuckets}}<tr><td>{{.Label}}</td><td class="num">{{.Count}}</td></tr>{{end}}
              </tbody>
            </table>
            {{end}}
          </details>
          {{end}}
        </li>
        {{end}}
      </ul>
    {{end}}
  {{else}}<p class="empty">No findings — clean run.</p>{{end}}

  <footer>tmula run report</footer>
</main>
</body>
</html>`

const compareHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Compare {{.A.ID}} vs {{.B.ID}}</title>
<style>` + baseStyle + `</style>
</head>
<body>
<main>
  <h1>Run comparison</h1>
  <div class="cols">
    <p class="meta">A · <code>{{.A.ID}}</code> <span class="pill status-{{.A.Status}}">{{.A.Status}}</span><br>mode {{.A.Mode}} · workers {{.A.Workers}}</p>
    <p class="meta">B · <code>{{.B.ID}}</code> <span class="pill status-{{.B.Status}}">{{.B.Status}}</span><br>mode {{.B.Mode}} · workers {{.B.Workers}}</p>
  </div>

  <h2>Metric deltas</h2>
  <table>
    <thead><tr><th>Metric</th><th class="num">A</th><th class="num">B</th><th class="num">Change</th></tr></thead>
    <tbody>
      {{range .Metrics}}
      <tr>
        <th>{{.Name}}</th>
        <td class="num">{{.A}}</td>
        <td class="num">{{.B}}</td>
        <td class="num dir-{{.Dir}}">{{.Change}}</td>
      </tr>
      {{end}}
    </tbody>
  </table>

  <h2>Findings diff</h2>

  <div class="diff-section diff-new">
    <h3><span class="tag tag-new">new</span> regressed or introduced in B ({{len .New}})</h3>
    {{if .New}}
    <ul class="findings">
      {{range .New}}<li class="finding"><span class="cat">{{.Category}}</span>{{if .EvidenceRef}}<span class="ref">· {{.EvidenceRef}}</span>{{end}}<div>{{.Description}}</div></li>{{end}}
    </ul>
    {{else}}<p class="empty">None.</p>{{end}}
  </div>

  <div class="diff-section diff-resolved">
    <h3><span class="tag tag-resolved">resolved</span> present in A, gone in B ({{len .Resolved}})</h3>
    {{if .Resolved}}
    <ul class="findings">
      {{range .Resolved}}<li class="finding"><span class="cat">{{.Category}}</span>{{if .EvidenceRef}}<span class="ref">· {{.EvidenceRef}}</span>{{end}}<div>{{.Description}}</div></li>{{end}}
    </ul>
    {{else}}<p class="empty">None.</p>{{end}}
  </div>

  <div class="diff-section diff-persist">
    <h3><span class="tag tag-persist">persisting</span> present in both ({{len .Persisted}})</h3>
    {{if .Persisted}}
    <ul class="findings">
      {{range .Persisted}}<li class="finding"><span class="cat">{{.A.Category}}</span>{{if .A.EvidenceRef}}<span class="ref">· {{.A.EvidenceRef}}</span>{{end}}<div>{{.A.Description}}{{if ne .A.Description .B.Description}} <span class="ref">→ B: {{.B.Description}}</span>{{end}}</div></li>{{end}}
    </ul>
    {{else}}<p class="empty">None.</p>{{end}}
  </div>

  <footer>tmula run comparison</footer>
</main>
</body>
</html>`
