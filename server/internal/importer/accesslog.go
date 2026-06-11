package importer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// FromAccessLog learns a behavior graph from real traffic: an access log in
// any of the supported formats (see the Format* constants — Apache/nginx
// combined, JSON lines including Caddy and Traefik shapes, AWS ALB, and
// CloudFront standard logs). Requests are grouped into sessions per client
// (IP + user agent, split on an idle gap), paths collapse into endpoints
// (/product/123 -> /product/{id}), and the transition frequencies between
// endpoints become the weighted edges of a graph-first scenario — a miniature
// of the observed traffic to replay against staging.
//
// Unlike FromOpenAPI/FromHAR this emits the graph-first form (Graph +
// Templates + Start), because learned journeys branch and a linear flow would
// lose exactly the structure that was learned. Logs carry no scheme/host, so
// Target is left blank and the caller must supply one.
//
// FromAccessLog applies the defaults; FromAccessLogWithOptions exposes the
// knobs (format hint, node cap, session gap, variable promotion).
func FromAccessLog(data []byte) (scenariofile.Scenario, AccessLogStats, error) {
	return FromAccessLogWithOptions(data, AccessLogOptions{})
}

// Format profile names, accepted as AccessLogOptions.Format and returned by
// DetectAccessLogFormat. Caddy and Traefik logs are JSON lines with
// well-known key spellings; they parse with the same tolerant JSON-lines
// parser but keep their own names so a detection or hint reports honestly
// which producer was recognized.
const (
	FormatCombined   = "combined"   // Apache/nginx common & combined text format
	FormatJSONLines  = "json"       // one JSON object per line, tolerant key names
	FormatALB        = "alb"        // AWS Application Load Balancer access log
	FormatCloudFront = "cloudfront" // CloudFront standard (access) log, tab-separated W3C
	FormatCaddy      = "caddy"      // Caddy structured access log (JSON lines)
	FormatTraefik    = "traefik"    // Traefik access log in JSON format
)

// Defaults applied by FromAccessLogWithOptions when an option is zero.
const (
	defaultMaxNodes           = 30
	defaultSessionGapSeconds  = 1800
	defaultMaxVariableSamples = 5
)

// maxSkippedSamples bounds the parse-failure diagnostics kept in the stats, so
// a hopeless file cannot bloat an import response.
const maxSkippedSamples = 10

// AccessLogOptions tunes the access-log learner. The zero value of every field
// keeps the documented default, so AccessLogOptions{} behaves exactly like
// FromAccessLog.
type AccessLogOptions struct {
	// Format forces a specific log format profile (one of the Format*
	// constants) instead of auto-detection. Use it when detection cannot see
	// the format — e.g. a CloudFront log whose first lines were trimmed.
	// Empty means detect from the content.
	Format string
	// MaxNodes caps how many of the hottest endpoints stay in the learned
	// graph; colder endpoints fold out and their transitions bridge across.
	// 0 means the default (30); a negative value disables the cap.
	MaxNodes int
	// SessionGapSeconds is the idle gap that splits one client's timeline
	// into separate sessions, mirroring how analytics define a visit.
	// 0 means the default (1800).
	SessionGapSeconds int
	// PromoteVariables, when set, promotes collapsed {id} path segments into
	// template variables ({{.product_id}}) instead of pinning each endpoint to
	// its single most-observed concrete path, and reports the observed value
	// pools in AccessLogStats.Variables. The variables use the load runtime's
	// template representation (load.Render), so a caller that seeds them into
	// the virtual users' Vars gets sessions that spread over the observed
	// resources. Off by default: a promoted scenario needs that seeding, while
	// the default concrete paths run as-is.
	//
	// Experimental: no seeding bridge exists yet, and load.Render templates
	// with missingkey=error, so enabling this without seeding the pools makes
	// every templated request fail to render.
	PromoteVariables bool
	// MaxVariableSamples caps the observed value pool kept per promoted
	// variable (hottest values first). 0 means the default (5).
	MaxVariableSamples int
}

// FromAccessLogWithOptions is FromAccessLog with the learner's knobs exposed;
// see AccessLogOptions for what each option does and its default.
func FromAccessLogWithOptions(data []byte, opts AccessLogOptions) (scenariofile.Scenario, AccessLogStats, error) {
	if opts.MaxNodes == 0 {
		opts.MaxNodes = defaultMaxNodes
	}
	if opts.SessionGapSeconds == 0 {
		opts.SessionGapSeconds = defaultSessionGapSeconds
	}
	if opts.MaxVariableSamples == 0 {
		opts.MaxVariableSamples = defaultMaxVariableSamples
	}
	return fromAccessLog(data, opts)
}

// LooksLikeAccessLog reports whether the data's leading lines parse as records
// of a supported access-log format. It exists for format auto-detection: an
// OpenAPI/HAR document never opens with such a line (a pretty-printed JSON
// document's first line is just "{", which does not parse as a record).
func LooksLikeAccessLog(data []byte) bool {
	_, ok := DetectAccessLogFormat(data)
	return ok
}

// detectProbeLines bounds how many leading non-empty lines detection examines:
// enough to step over a line truncated by log rotation, cheap enough to run on
// every upload.
const detectProbeLines = 5

// DetectAccessLogFormat sniffs which supported access-log format the data is
// in, returning one of the Format* constants. It probes the first few
// non-empty lines (a rotated file may open with a truncated line) and reports
// ok=false when none of them parses as a log record.
func DetectAccessLogFormat(data []byte) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	probed := 0
	for sc.Scan() && probed < detectProbeLines {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		probed++
		if f, ok := detectLineFormat(line); ok {
			return f, true
		}
	}
	return "", false
}

// detectLineFormat classifies a single line into a format profile.
func detectLineFormat(line string) (string, bool) {
	switch {
	case strings.HasPrefix(line, "#"):
		// CloudFront standard log files open with #Version / #Fields directives.
		if strings.HasPrefix(line, "#Version") || strings.HasPrefix(line, "#Fields") {
			return FormatCloudFront, true
		}
		return "", false
	case strings.HasPrefix(line, "{"):
		return detectJSONFormat(line)
	}
	// An ALB entry opens with a connection type and an ISO 8601 timestamp;
	// checking just those two tokens keeps detection independent of whether
	// the rest of the entry is intact.
	if fields := strings.Fields(line); len(fields) >= 13 && albTypes[fields[0]] {
		if _, err := time.Parse(time.RFC3339, fields[1]); err == nil {
			return FormatALB, true
		}
	}
	if _, err := (combinedParser{}).parse(line); err == nil {
		return FormatCombined, true
	}
	return "", false
}

// detectJSONFormat distinguishes the well-known JSON-lines producers so the
// stats report which one was recognized; they all parse with the same
// tolerant parser.
func detectJSONFormat(line string) (string, bool) {
	if _, err := parseJSONLine(line); err != nil {
		return "", false
	}
	var probe struct {
		RequestMethod string          `json:"RequestMethod"` // Traefik's key spelling
		Logger        string          `json:"logger"`        // Caddy: "http.log.access"
		Request       json.RawMessage `json:"request"`       // Caddy nests the request fields
	}
	_ = json.Unmarshal([]byte(line), &probe)
	switch {
	case probe.RequestMethod != "":
		return FormatTraefik, true
	case probe.Logger == "http.log.access" || (len(probe.Request) > 0 && probe.Request[0] == '{'):
		return FormatCaddy, true
	}
	return FormatJSONLines, true
}

// AccessLogStats reports what the learner kept and dropped, so a capped or
// noisy import is visible instead of silently passing as full coverage.
type AccessLogStats struct {
	// Format is the resolved format profile (a Format* constant): the explicit
	// hint when one was given, else what detection recognized.
	Format string
	// Requests is the number of usable log records (parsed, non-asset).
	Requests int
	// Skipped counts lines that did not parse or were filtered out (assets,
	// unsupported methods).
	Skipped int
	// SkippedSamples holds up to ten of the skipped lines that failed to
	// parse — line number, a 120-char text prefix, and the reason — so a
	// half-broken real-world log reports why coverage is partial instead of a
	// bare count. Lines filtered by design (assets, non-journey methods) are
	// counted in Skipped but never sampled here.
	SkippedSamples []SkippedLine
	// Sessions is the number of per-client visits after gap splitting.
	Sessions int
	// Clients is the number of distinct IP + user-agent identities.
	Clients int
	// DroppedEndpoints counts endpoints beyond the node cap that were folded
	// out of the graph (their transitions bridge across them).
	DroppedEndpoints int
	// Variables lists the template variables promoted out of collapsed {id}
	// path segments with their observed sample pools. Populated only with
	// AccessLogOptions.PromoteVariables; the caller seeds these pools into the
	// virtual users' Vars (the load runtime's existing variable system) so
	// sessions spread over the observed resources.
	Variables []PromotedVariable
}

// SkippedLine is one sampled parse failure, small enough to render in a
// diagnostic table. It mirrors the wire shape the import endpoint reports
// (api.ImportSkippedSample) so the mapping stays field-for-field.
type SkippedLine struct {
	// Line is the 1-based line number in the input.
	Line int
	// Text is the line's first 120 characters.
	Text string
	// Reason says why the line could not be parsed.
	Reason string
}

// PromotedVariable is one template variable the learner promoted out of a
// collapsed {id} path segment, named after the segment before it
// (/product/{id} -> product_id) and shared across every endpoint that uses
// the same name — /product/{id} and /product/{id}/reviews draw from one pool.
type PromotedVariable struct {
	// Name is the variable as it appears in template paths: {{.Name}}.
	Name string
	// Values is the observed concrete value pool, hottest first, capped at
	// AccessLogOptions.MaxVariableSamples.
	Values []string
}

// logRecord is one usable request observation.
type logRecord struct {
	time   time.Time
	client string // IP + user agent
	method string
	path   string // raw path (with query)
}

func fromAccessLog(data []byte, opts AccessLogOptions) (scenariofile.Scenario, AccessLogStats, error) {
	var stats AccessLogStats

	format := opts.Format
	if format == "" {
		detected, ok := DetectAccessLogFormat(data)
		if !ok {
			return scenariofile.Scenario{}, stats, fmt.Errorf(
				"importer: access log format not recognized (supported: %s, %s, %s, %s, %s, %s)",
				FormatCombined, FormatJSONLines, FormatALB, FormatCloudFront, FormatCaddy, FormatTraefik)
		}
		format = detected
	}
	parser, err := parserFor(format)
	if err != nil {
		return scenariofile.Scenario{}, stats, err
	}
	stats.Format = format

	var records []logRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		rec, perr := parser.parse(line)
		if perr != nil {
			if errors.Is(perr, errDirective) {
				continue // structural line (CloudFront #Fields), not data
			}
			stats.Skipped++
			if len(stats.SkippedSamples) < maxSkippedSamples {
				stats.SkippedSamples = append(stats.SkippedSamples, SkippedLine{
					Line:   lineNo,
					Text:   truncateRunes(line, 120),
					Reason: perr.Error(),
				})
			}
			continue
		}
		if !keepRequest(rec.method, rec.path) {
			// Filtered by design (asset, non-journey method): counted so the
			// coverage is honest, but not a parse failure worth a sample.
			stats.Skipped++
			continue
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		return scenariofile.Scenario{}, stats, fmt.Errorf("importer: read access log: %w", err)
	}
	if len(records) == 0 {
		// Surface the first diagnostic in the error itself: the CLI shows only
		// the error, and "why line 1 failed" is the actionable part.
		if len(stats.SkippedSamples) > 0 {
			s := stats.SkippedSamples[0]
			return scenariofile.Scenario{}, stats, fmt.Errorf("importer: access log has no usable requests (line %d: %s)", s.Line, s.Reason)
		}
		return scenariofile.Scenario{}, stats, fmt.Errorf("importer: access log has no usable requests")
	}
	stats.Requests = len(records)

	sessions, clients := sessionize(records, time.Duration(opts.SessionGapSeconds)*time.Second)
	stats.Sessions = len(sessions)
	stats.Clients = clients

	// Collapse raw paths into endpoints and keep the hottest MaxNodes of them;
	// requests to folded endpoints are removed from each session so the
	// surviving transitions bridge across them (the standard reduction for an
	// over-wide transition model).
	kept, dropped := capEndpoints(records, opts.MaxNodes)
	stats.DroppedEndpoints = dropped

	g := buildLearnedGraph(sessions, kept, promoteOptions{
		enabled:    opts.PromoteVariables,
		maxSamples: opts.MaxVariableSamples,
	})
	stats.Variables = g.variables

	scn := scenariofile.Scenario{
		Graph:     &g.graph,
		Templates: g.templates,
		Start:     g.start,
		MaxSteps:  g.maxSteps,
		Open:      suggestOpen(records, sessions),
	}
	return scn, stats, nil
}

// --- line parsing ---

// lineParser turns one raw log line into a logRecord. The returned error is
// the human-readable skip reason; errDirective marks a structural line that is
// neither a record nor a failure. Implementations may keep state across lines
// (CloudFront's #Fields directive defines the column order for what follows),
// so a parser instance belongs to a single file scan.
type lineParser interface {
	parse(line string) (logRecord, error)
}

// errDirective marks a line that is file structure rather than data, e.g.
// CloudFront's #Version/#Fields headers: consumed silently, never counted as
// skipped.
var errDirective = errors.New("directive line")

// parserFor maps a format profile onto its parser. The caddy and traefik
// profiles share the tolerant JSON-lines parser, whose key spellings cover
// both producers.
func parserFor(format string) (lineParser, error) {
	switch format {
	case FormatCombined:
		return combinedParser{}, nil
	case FormatJSONLines, FormatCaddy, FormatTraefik:
		return jsonParser{}, nil
	case FormatALB:
		return albParser{}, nil
	case FormatCloudFront:
		return &cloudFrontParser{}, nil
	}
	return nil, fmt.Errorf("importer: unknown access log format %q (supported: %s, %s, %s, %s, %s, %s)",
		format, FormatCombined, FormatJSONLines, FormatALB, FormatCloudFront, FormatCaddy, FormatTraefik)
}

// combinedRE matches the Apache/nginx common and combined log formats:
//
//	host ident user [time] "METHOD path proto" status bytes ["referer" "ua"]
var combinedRE = regexp.MustCompile(
	`^(\S+) \S+ \S+ \[([^\]]+)\] "([A-Za-z]+) (\S+)[^"]*" (\d{3}) \S+(?: "[^"]*" "([^"]*)")?`)

const combinedTimeLayout = "02/Jan/2006:15:04:05 -0700"

// combinedParser parses the Apache/nginx common and combined text formats.
type combinedParser struct{}

func (combinedParser) parse(line string) (logRecord, error) {
	m := combinedRE.FindStringSubmatch(line)
	if m == nil {
		return logRecord{}, fmt.Errorf("does not match the Apache/nginx combined log format")
	}
	t, err := time.Parse(combinedTimeLayout, m[2])
	if err != nil {
		return logRecord{}, fmt.Errorf("timestamp %q does not parse as %s", m[2], combinedTimeLayout)
	}
	return logRecord{
		time:   t,
		client: m[1] + "\x00" + m[6],
		method: strings.ToUpper(m[3]),
		path:   m[4],
	}, nil
}

// jsonParser parses one-JSON-object-per-line logs with tolerant key names,
// covering generic app/nginx/envoy JSON logs as well as Caddy's structured
// access logs and Traefik's JSON access logs.
type jsonParser struct{}

func (jsonParser) parse(line string) (logRecord, error) { return parseJSONLine(line) }

// jsonLogLine accepts the common key spellings across JSON access loggers, so
// a JSON log works without a mapping config. Either split method/path keys or
// a combined "request" line is fine. The Traefik spellings (StartUTC,
// RequestMethod, RequestPath, ClientHost, request_User-Agent) follow
// https://doc.traefik.io/traefik/reference/install-configuration/observability/logs-and-accesslogs/
// — headers are flattened with a request_ prefix. Caddy nests its request
// fields under "request" (see caddyRequest).
type jsonLogLine struct {
	Time      json.RawMessage `json:"time"`
	Ts        json.RawMessage `json:"ts"`
	Timestamp json.RawMessage `json:"timestamp"`
	AtTime    json.RawMessage `json:"@timestamp"`
	StartUTC  json.RawMessage `json:"StartUTC"`

	Method        string `json:"method"`
	RequestMethod string `json:"request_method"`
	TraefikMethod string `json:"RequestMethod"`
	Path          string `json:"path"`
	URI           string `json:"uri"`
	RequestURI    string `json:"request_uri"`
	URL           string `json:"url"`
	TraefikPath   string `json:"RequestPath"`
	// Request is either a combined "GET /path HTTP/1.1" string (nginx-style
	// JSON logs) or Caddy's nested request object.
	Request json.RawMessage `json:"request"`

	RemoteAddr string `json:"remote_addr"`
	ClientIP   string `json:"client_ip"`
	IP         string `json:"ip"`
	ClientHost string `json:"ClientHost"`

	UserAgent     string `json:"user_agent"`
	HTTPUserAgent string `json:"http_user_agent"`
	UA            string `json:"ua"`
	TraefikUA     string `json:"request_User-Agent"`
}

// caddyRequest is the nested request object in Caddy's structured access log
// (logger "http.log.access"): remote_ip/client_ip, method, uri (path with
// query), and the request headers as name -> values. The remote_addr spelling
// covers Caddy v2.4 and earlier, which logged a single ip:port field.
// Source: https://caddyserver.com/docs/logging#structured-logs
type caddyRequest struct {
	RemoteIP   string              `json:"remote_ip"`
	RemoteAddr string              `json:"remote_addr"`
	ClientIP   string              `json:"client_ip"`
	Method     string              `json:"method"`
	URI        string              `json:"uri"`
	Headers    map[string][]string `json:"headers"`
}

func parseJSONLine(line string) (logRecord, error) {
	var j jsonLogLine
	if err := json.Unmarshal([]byte(line), &j); err != nil {
		return logRecord{}, fmt.Errorf("not a valid JSON log line")
	}

	// The "request" key is polymorphic: a combined request-line string, or
	// Caddy's nested object.
	var requestLine string
	var caddy caddyRequest
	if len(j.Request) > 0 {
		switch j.Request[0] {
		case '"':
			_ = json.Unmarshal(j.Request, &requestLine)
		case '{':
			_ = json.Unmarshal(j.Request, &caddy)
		}
	}

	t, ok := parseJSONTime(firstRaw(j.Time, j.Ts, j.Timestamp, j.AtTime, j.StartUTC))
	if !ok {
		return logRecord{}, fmt.Errorf("no recognizable timestamp (time, ts, timestamp, @timestamp, StartUTC)")
	}

	method := firstNonEmpty(j.Method, j.RequestMethod, j.TraefikMethod, caddy.Method)
	path := firstNonEmpty(j.Path, j.URI, j.RequestURI, j.URL, j.TraefikPath, caddy.URI)
	if (method == "" || path == "") && requestLine != "" {
		fields := strings.Fields(requestLine)
		if len(fields) >= 2 {
			method, path = fields[0], fields[1]
		}
	}
	if method == "" || path == "" || !strings.HasPrefix(path, "/") {
		return logRecord{}, fmt.Errorf("no usable method/path keys (method, path, uri, url, RequestPath, request)")
	}

	ip := firstNonEmpty(j.RemoteAddr, j.ClientIP, j.IP, j.ClientHost, caddy.ClientIP, caddy.RemoteIP, stripPort(caddy.RemoteAddr))
	ua := firstNonEmpty(j.UserAgent, j.HTTPUserAgent, j.UA, j.TraefikUA, firstHeader(caddy.Headers, "User-Agent"))
	return logRecord{
		time:   t,
		client: ip + "\x00" + ua,
		method: strings.ToUpper(method),
		path:   path,
	}, nil
}

// albTypes are the connection types an ALB entry can open with (the type
// field, position 1); used by both parsing and detection.
var albTypes = map[string]bool{
	"http": true, "https": true, "h2": true, "grpcs": true, "ws": true, "wss": true,
}

// albParser parses AWS Application Load Balancer access logs: space-delimited
// fields with the request line and user agent as double-quoted fields. The
// positions used here are type (1), time (2, ISO 8601), client:port (4),
// "request" (13, `METHOD protocol://host:port/uri HTTP-version`) and
// "user_agent" (14); trailing fields vary by feature and release, so parsing
// stops after the user agent.
// Source: https://docs.aws.amazon.com/elasticloadbalancing/latest/application/load-balancer-access-logs.html
type albParser struct{}

func (albParser) parse(line string) (logRecord, error) {
	fields := splitQuoted(line)
	if len(fields) < 14 {
		return logRecord{}, fmt.Errorf("alb entry has %d fields, need at least 14 (type through user_agent)", len(fields))
	}
	if !albTypes[fields[0]] {
		return logRecord{}, fmt.Errorf("alb entry opens with %q, not a connection type (http, https, h2, grpcs, ws, wss)", fields[0])
	}
	t, err := time.Parse(time.RFC3339, fields[1])
	if err != nil {
		return logRecord{}, fmt.Errorf("alb timestamp %q is not ISO 8601", fields[1])
	}
	method, path, err := splitALBRequest(fields[12])
	if err != nil {
		return logRecord{}, err
	}
	ua := fields[13]
	if ua == "-" {
		ua = ""
	}
	return logRecord{
		time:   t,
		client: stripPort(fields[3]) + "\x00" + ua,
		method: strings.ToUpper(method),
		path:   path,
	}, nil
}

// splitALBRequest splits the quoted ALB request field. Unlike the combined
// format the URL is absolute (protocol://host:port/uri), so the path is
// extracted from it; a malformed client request is logged as "- - -" and
// rejected here.
func splitALBRequest(req string) (method, path string, err error) {
	parts := strings.Fields(req)
	if len(parts) < 2 || parts[0] == "-" {
		return "", "", fmt.Errorf("alb request field %q is not \"METHOD url protocol\"", req)
	}
	u, perr := url.Parse(parts[1])
	if perr != nil {
		return "", "", fmt.Errorf("alb request url %q does not parse", parts[1])
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return parts[0], p, nil
}

// splitQuoted splits a line on spaces, keeping double-quoted runs (which may
// contain spaces) together as single fields with the quotes stripped. ALB does
// not escape embedded quotes, so none are unescaped here.
func splitQuoted(line string) []string {
	var fields []string
	var b strings.Builder
	inQuote, quoted := false, false
	flush := func() {
		if b.Len() > 0 || quoted {
			fields = append(fields, b.String())
		}
		b.Reset()
		quoted = false
	}
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '"':
			inQuote = !inQuote
			quoted = true
		case c == ' ' && !inQuote:
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return fields
}

// cloudFrontParser parses CloudFront standard (access) log files: tab-separated
// columns whose order is declared by a leading "#Fields:" directive (W3C
// extended log format). The columns used are date (YYYY-MM-DD, UTC), time
// (HH:MM:SS, UTC), c-ip, cs-method, cs-uri-stem (path without the query),
// cs-uri-query ("-" when absent) and cs(User-Agent) (URL-encoded). The parser
// is header-driven, so it follows whatever column order the file declares.
// Source: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/standard-logs-reference.html
type cloudFrontParser struct {
	cols map[string]int // column name -> index, from the #Fields directive
	n    int            // declared column count
}

const cloudFrontTimeLayout = "2006-01-02 15:04:05"

func (p *cloudFrontParser) parse(line string) (logRecord, error) {
	if strings.HasPrefix(line, "#") {
		if rest, ok := strings.CutPrefix(line, "#Fields:"); ok {
			names := strings.Fields(rest)
			p.cols = make(map[string]int, len(names))
			for i, name := range names {
				p.cols[name] = i
			}
			p.n = len(names)
		}
		return logRecord{}, errDirective // #Version and friends carry no data
	}
	if p.cols == nil {
		return logRecord{}, fmt.Errorf("cloudfront data line before the #Fields header (column order unknown)")
	}
	cells := strings.Split(line, "\t")
	if len(cells) < p.n {
		return logRecord{}, fmt.Errorf("cloudfront entry has %d columns, the #Fields header declares %d", len(cells), p.n)
	}
	get := func(name string) (string, bool) {
		i, ok := p.cols[name]
		if !ok {
			return "", false
		}
		v := strings.TrimSpace(cells[i])
		if v == "-" {
			v = "" // "-" is the documented empty marker
		}
		return v, true
	}
	date, ok1 := get("date")
	tm, ok2 := get("time")
	method, ok3 := get("cs-method")
	stem, ok4 := get("cs-uri-stem")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return logRecord{}, fmt.Errorf("cloudfront #Fields header lacks one of date, time, cs-method, cs-uri-stem")
	}
	t, err := time.Parse(cloudFrontTimeLayout, date+" "+tm)
	if err != nil {
		return logRecord{}, fmt.Errorf("cloudfront timestamp %q does not parse", date+" "+tm)
	}
	if !strings.HasPrefix(stem, "/") {
		return logRecord{}, fmt.Errorf("cloudfront cs-uri-stem %q is not a path", stem)
	}
	if q, _ := get("cs-uri-query"); q != "" {
		stem += "?" + q // the stem never carries the query; reattach it
	}
	ip, _ := get("c-ip")
	ua, _ := get("cs(User-Agent)")
	// The user agent is URL-encoded in the log; decode best-effort for a
	// faithful client identity (the raw value still groups fine if not).
	if dec, derr := url.PathUnescape(ua); derr == nil {
		ua = dec
	}
	return logRecord{
		time:   t,
		client: ip + "\x00" + ua,
		method: strings.ToUpper(method),
		path:   stem,
	}, nil
}

// parseJSONTime reads RFC3339 strings and unix seconds/milliseconds (numeric
// or numeric-string).
func parseJSONTime(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 {
		return time.Time{}, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return unixTime(n), true
		}
		return time.Time{}, false
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return unixTime(n), true
	}
	return time.Time{}, false
}

// unixTime interprets a numeric timestamp as unix seconds, or milliseconds
// when it is too large to be a plausible seconds value.
func unixTime(n float64) time.Time {
	const msThreshold = 1e12 // ~year 33658 in seconds; any real ms value exceeds it
	if n >= msThreshold {
		return time.UnixMilli(int64(n))
	}
	sec, frac := math.Modf(n)
	return time.Unix(int64(sec), int64(frac*1e9))
}

func firstRaw(raws ...json.RawMessage) json.RawMessage {
	for _, r := range raws {
		if len(r) > 0 {
			return r
		}
	}
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// firstHeader returns the first value of a header in a name -> values map
// (Caddy's request.headers shape).
func firstHeader(headers map[string][]string, name string) string {
	if vs := headers[name]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// stripPort removes a trailing :port from an ip:port value (the port is all
// digits); anything else — including a bare IP — passes through unchanged.
func stripPort(s string) string {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return s
	}
	for _, r := range s[i+1:] {
		if r < '0' || r > '9' {
			return s
		}
	}
	return s[:i]
}

// truncateRunes returns at most n runes of s, never splitting a UTF-8
// sequence, so a sampled line cannot bloat the stats.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// keepRequest filters out records that do not represent user behavior: static
// assets and non-journey methods.
func keepRequest(method, path string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return false
	}
	p := path
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if i := strings.LastIndex(p, "."); i >= 0 {
		switch strings.ToLower(p[i:]) {
		case ".css", ".js", ".mjs", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg",
			".ico", ".woff", ".woff2", ".ttf", ".eot", ".webp", ".avif", ".mp4", ".webm":
			return false
		}
	}
	return true
}

// --- sessionization ---

// sessionize groups records by client and splits each client's timeline into
// sessions on an idle gap, mirroring how analytics define a visit.
func sessionize(records []logRecord, gap time.Duration) ([][]logRecord, int) {
	byClient := make(map[string][]logRecord)
	for _, r := range records {
		byClient[r.client] = append(byClient[r.client], r)
	}

	// Deterministic session order regardless of map iteration.
	clientKeys := make([]string, 0, len(byClient))
	for k := range byClient {
		clientKeys = append(clientKeys, k)
	}
	sort.Strings(clientKeys)

	var sessions [][]logRecord
	for _, k := range clientKeys {
		recs := byClient[k]
		sort.SliceStable(recs, func(i, j int) bool { return recs[i].time.Before(recs[j].time) })
		cur := []logRecord{recs[0]}
		for _, r := range recs[1:] {
			if r.time.Sub(cur[len(cur)-1].time) > gap {
				sessions = append(sessions, cur)
				cur = nil
			}
			cur = append(cur, r)
		}
		sessions = append(sessions, cur)
	}
	return sessions, len(byClient)
}

// --- endpoint collapsing ---

// endpointKey collapses a raw request into its endpoint identity: the method
// plus the path with volatile segments (numbers, UUIDs, long hex ids)
// replaced by {id} and the query stripped.
func endpointKey(method, rawPath string) string {
	p := rawPath
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if volatileSegment(s) {
			segs[i] = "{id}"
		}
	}
	return method + " " + strings.Join(segs, "/")
}

var (
	numericRE = regexp.MustCompile(`^[0-9]+$`)
	uuidRE    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexRE     = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
)

func volatileSegment(s string) bool {
	return numericRE.MatchString(s) || uuidRE.MatchString(s) || hexRE.MatchString(s)
}

// capEndpoints ranks endpoints by request volume and keeps the top maxNodes.
// It returns the kept set and how many endpoints were folded out.
func capEndpoints(records []logRecord, maxNodes int) (map[string]bool, int) {
	counts := make(map[string]int)
	for _, r := range records {
		counts[endpointKey(r.method, r.path)]++
	}
	if maxNodes <= 0 || len(counts) <= maxNodes {
		kept := make(map[string]bool, len(counts))
		for k := range counts {
			kept[k] = true
		}
		return kept, 0
	}
	type kc struct {
		key   string
		count int
	}
	ranked := make([]kc, 0, len(counts))
	for k, c := range counts {
		ranked = append(ranked, kc{k, c})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].key < ranked[j].key
	})
	kept := make(map[string]bool, maxNodes)
	for _, r := range ranked[:maxNodes] {
		kept[r.key] = true
	}
	return kept, len(ranked) - maxNodes
}

// --- graph assembly ---

type learnedGraph struct {
	graph     domain.ScenarioGraph
	templates map[domain.ID]domain.APITemplate
	start     string
	maxSteps  int
	variables []PromotedVariable
}

// promoteOptions carries the variable-promotion knobs into the graph builder.
type promoteOptions struct {
	enabled    bool
	maxSamples int
}

// buildLearnedGraph turns the sessions into a weighted behavior graph over the
// kept endpoints: transition counts normalize into per-node edge weights, each
// session's last request earns an edge into the terminal exit node, the most
// common session start becomes the start node, and maxSteps tracks the p95
// session length.
func buildLearnedGraph(sessions [][]logRecord, kept map[string]bool, promo promoteOptions) learnedGraph {
	type transition struct{ from, to string }
	transitions := make(map[transition]int)
	startCounts := make(map[string]int)
	endpointOrder := []string{}
	seen := make(map[string]bool)
	// Per endpoint, the concrete observed request lines, to pick a runnable
	// template path (logs have no bodies, so the path is all we can carry).
	concrete := make(map[string]map[string]int)
	var lengths []int

	for _, sess := range sessions {
		var walk []logRecord
		for _, r := range sess {
			if kept[endpointKey(r.method, r.path)] {
				walk = append(walk, r)
			}
		}
		if len(walk) == 0 {
			continue
		}
		lengths = append(lengths, len(walk))
		for i, r := range walk {
			key := endpointKey(r.method, r.path)
			if !seen[key] {
				seen[key] = true
				endpointOrder = append(endpointOrder, key)
			}
			if concrete[key] == nil {
				concrete[key] = make(map[string]int)
			}
			concrete[key][r.path]++
			if i == 0 {
				startCounts[key]++
			}
			if i+1 < len(walk) {
				transitions[transition{key, endpointKey(walk[i+1].method, walk[i+1].path)}]++
			} else {
				transitions[transition{key, "exit"}]++
			}
		}
	}

	// Stable, journey-ish node order: first-seen order across sessions.
	ids := newIDSet()
	nodeID := make(map[string]string, len(endpointOrder))
	var nodes []domain.Node
	templates := make(map[domain.ID]domain.APITemplate, len(endpointOrder))
	// pools accumulates observed values per promoted variable name, merged
	// across endpoints so /product/{id} and /product/{id}/reviews share one
	// product_id pool.
	pools := make(map[string]map[string]int)
	for _, key := range endpointOrder {
		method, pattern, _ := strings.Cut(key, " ")
		id := ids.unique(sanitize(strings.ToLower(method) + "_" + pattern))
		nodeID[key] = id
		tmplID := domain.ID("t_" + id)
		nodes = append(nodes, domain.Node{ID: domain.ID(id), APITemplateID: tmplID})
		path := mostObserved(concrete[key])
		if promo.enabled {
			if promoted, varPools := promotePath(pattern, path, concrete[key]); len(varPools) > 0 {
				path = promoted
				for _, vp := range varPools {
					if pools[vp.name] == nil {
						pools[vp.name] = make(map[string]int)
					}
					for v, c := range vp.counts {
						pools[vp.name][v] += c
					}
				}
			}
		}
		templates[tmplID] = domain.APITemplate{
			ID:       tmplID,
			Protocol: domain.ProtocolREST,
			Method:   method,
			Path:     path,
		}
	}
	nodes = append(nodes, domain.Node{ID: "exit"})
	nodeID["exit"] = "exit"

	// Outgoing counts normalize into proportions; round for a readable file and
	// shave any float overshoot off the largest edge so the strict per-node
	// sum <= 1 rule always holds.
	outTotals := make(map[string]int)
	for tr, c := range transitions {
		outTotals[tr.from] += c
	}
	var edges []domain.Edge
	for tr, c := range transitions {
		edges = append(edges, domain.Edge{
			From:   domain.ID(nodeID[tr.from]),
			To:     domain.ID(nodeID[tr.to]),
			Weight: math.Round(float64(c)/float64(outTotals[tr.from])*1e4) / 1e4,
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	shaveOvershoot(edges)

	start := ""
	for key, c := range startCounts {
		if start == "" || c > startCounts[start] || (c == startCounts[start] && key < start) {
			start = key
		}
	}

	var variables []PromotedVariable
	if len(pools) > 0 {
		names := make([]string, 0, len(pools))
		for name := range pools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			variables = append(variables, PromotedVariable{Name: name, Values: topValues(pools[name], promo.maxSamples)})
		}
	}

	return learnedGraph{
		graph:     domain.ScenarioGraph{ID: "learned", Nodes: nodes, Edges: edges},
		templates: templates,
		start:     nodeID[start],
		maxSteps:  clamp(percentileInt(lengths, 0.95), 4, 100),
		variables: variables,
	}
}

// --- variable promotion ---

// pathVarPool is one promoted variable in one endpoint: its name and the
// observed concrete values (with counts) at its path position.
type pathVarPool struct {
	name   string
	counts map[string]int
}

// promotePath rewrites an endpoint's template path so each collapsed {id}
// segment becomes a template variable in the load runtime's representation
// ({{.product_id}}), and collects the observed value pool per variable. The
// rewrite starts from the most-observed concrete path, so anything that is not
// a collapsed segment — including the query string — is preserved verbatim. A
// pattern without {id} segments returns unchanged with no pools.
func promotePath(pattern, concrete string, observed map[string]int) (string, []pathVarPool) {
	patSegs := strings.Split(pattern, "/")
	// Name the variable at each volatile position; duplicates within one
	// endpoint get a numeric suffix because one render uses one value per name.
	names := make(map[int]string)
	var positions []int
	seen := make(map[string]int)
	for i, s := range patSegs {
		if s != "{id}" {
			continue
		}
		name := variableName(patSegs, i)
		seen[name]++
		if n := seen[name]; n > 1 {
			name = fmt.Sprintf("%s_%d", name, n)
		}
		names[i] = name
		positions = append(positions, i)
	}
	if len(positions) == 0 {
		return concrete, nil
	}

	// Pool the concrete values seen at each volatile position, weighted by how
	// often each concrete path was observed.
	counts := make(map[int]map[string]int, len(positions))
	for raw, c := range observed {
		p := raw
		if j := strings.IndexAny(p, "?#"); j >= 0 {
			p = p[:j]
		}
		segs := strings.Split(p, "/")
		if len(segs) != len(patSegs) {
			continue // defensive: every observed path matches its own pattern
		}
		for _, i := range positions {
			if counts[i] == nil {
				counts[i] = make(map[string]int)
			}
			counts[i][segs[i]] += c
		}
	}

	// Rewrite the most-observed concrete path, keeping its query suffix.
	pathPart, suffix := concrete, ""
	if j := strings.IndexAny(concrete, "?#"); j >= 0 {
		pathPart, suffix = concrete[:j], concrete[j:]
	}
	segs := strings.Split(pathPart, "/")
	out := make([]pathVarPool, 0, len(positions))
	for _, i := range positions {
		if i < len(segs) {
			segs[i] = "{{." + names[i] + "}}"
		}
		out = append(out, pathVarPool{name: names[i], counts: counts[i]})
	}
	return strings.Join(segs, "/") + suffix, out
}

// variableName derives a template variable name for the volatile segment at
// position i of a collapsed pattern from the segment before it:
// /product/{id} -> product_id, /users/{id}/orders/{id} -> users_id, orders_id.
// The name must be a valid Go template identifier (load.Render parses paths
// with text/template), so it falls back to "id" without a usable prefix and
// gains a "v" prefix when it would start with a digit.
func variableName(patSegs []string, i int) string {
	name := "id"
	if i > 0 && patSegs[i-1] != "" && patSegs[i-1] != "{id}" {
		name = sanitize(strings.ToLower(patSegs[i-1])) + "_id"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "v" + name
	}
	return name
}

// topValues ranks a value pool by observation count (ties break
// lexicographically for determinism) and keeps the hottest n.
func topValues(counts map[string]int, n int) []string {
	type vc struct {
		v string
		c int
	}
	ranked := make([]vc, 0, len(counts))
	for v, c := range counts {
		ranked = append(ranked, vc{v, c})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].c != ranked[j].c {
			return ranked[i].c > ranked[j].c
		}
		return ranked[i].v < ranked[j].v
	})
	if n > 0 && len(ranked) > n {
		ranked = ranked[:n]
	}
	out := make([]string, len(ranked))
	for i, r := range ranked {
		out[i] = r.v
	}
	return out
}

// shaveOvershoot subtracts any rounded per-node weight overshoot beyond 1.0
// from that node's largest edge.
func shaveOvershoot(edges []domain.Edge) {
	sums := make(map[domain.ID]float64)
	largest := make(map[domain.ID]int)
	for i, e := range edges {
		sums[e.From] += e.Weight
		if e.Weight > edges[largest[e.From]].Weight || edges[largest[e.From]].From != e.From {
			largest[e.From] = i
		}
	}
	for from, s := range sums {
		if s > 1 {
			edges[largest[from]].Weight = math.Round((edges[largest[from]].Weight-(s-1))*1e4) / 1e4
		}
	}
}

// mostObserved returns the most frequent concrete path (ties break
// lexicographically for determinism).
func mostObserved(counts map[string]int) string {
	best := ""
	for p, c := range counts {
		if best == "" || c > counts[best] || (c == counts[best] && p < best) {
			best = p
		}
	}
	return best
}

// suggestOpen derives an open-workload suggestion from the observed traffic:
// the average session arrival rate (floored at 1/s so a sparse log still
// yields a run that does something) and a think-time range from the
// inter-request gap quartiles.
func suggestOpen(records []logRecord, sessions [][]logRecord) *scenariofile.Open {
	first, last := records[0].time, records[0].time
	for _, r := range records[1:] {
		if r.time.Before(first) {
			first = r.time
		}
		if r.time.After(last) {
			last = r.time
		}
	}
	span := last.Sub(first).Seconds()
	rate := 1.0
	if span > 0 {
		if observed := float64(len(sessions)) / span; observed > rate {
			rate = math.Round(observed*100) / 100
		}
	}

	var gapsMs []int
	for _, sess := range sessions {
		for i := 1; i < len(sess); i++ {
			gapsMs = append(gapsMs, int(sess[i].time.Sub(sess[i-1].time).Milliseconds()))
		}
	}
	think := []int{200, 800}
	if len(gapsMs) > 0 {
		sort.Ints(gapsMs)
		think = []int{
			clamp(percentileSorted(gapsMs, 0.25), 0, 30000),
			clamp(percentileSorted(gapsMs, 0.75), 0, 30000),
		}
	}

	return &scenariofile.Open{Rate: rate, ForSeconds: 60, ThinkMs: think}
}

func percentileInt(values []int, p float64) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return percentileSorted(sorted, p)
}

func percentileSorted(sorted []int, p float64) int {
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
