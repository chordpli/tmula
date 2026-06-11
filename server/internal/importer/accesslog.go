package importer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// FromAccessLog learns a behavior graph from real traffic: an access log in
// Apache/nginx combined format or JSON lines. Requests are grouped into
// sessions per client (IP + user agent, split on an idle gap), paths collapse
// into endpoints (/product/123 -> /product/{id}), and the transition
// frequencies between endpoints become the weighted edges of a graph-first
// scenario — a miniature of the observed traffic to replay against staging.
//
// Unlike FromOpenAPI/FromHAR this emits the graph-first form (Graph +
// Templates + Start), because learned journeys branch and a linear flow would
// lose exactly the structure that was learned. Logs carry no scheme/host, so
// Target is left blank and the caller must supply one.
func FromAccessLog(data []byte) (scenariofile.Scenario, AccessLogStats, error) {
	return fromAccessLog(data, accessLogOptions{
		maxNodes:          30,
		sessionGapSeconds: 1800,
	})
}

// LooksLikeAccessLog reports whether the data's first non-empty line parses as
// an access-log record (combined format or a JSON log line). It exists for
// format auto-detection: an OpenAPI/HAR document never starts with such a line
// (a pretty-printed JSON document's first line is just "{", which does not
// parse as a record).
func LooksLikeAccessLog(data []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		_, ok := parseLogLine(line)
		return ok
	}
	return false
}

// AccessLogStats reports what the learner kept and dropped, so a capped or
// noisy import is visible instead of silently passing as full coverage.
type AccessLogStats struct {
	// Requests is the number of usable log records (parsed, non-asset).
	Requests int
	// Skipped counts lines that did not parse or were filtered out (assets,
	// unsupported methods).
	Skipped int
	// Sessions is the number of per-client visits after gap splitting.
	Sessions int
	// Clients is the number of distinct IP + user-agent identities.
	Clients int
	// DroppedEndpoints counts endpoints beyond the node cap that were folded
	// out of the graph (their transitions bridge across them).
	DroppedEndpoints int
}

// accessLogOptions parameterizes the learner for tests; FromAccessLog applies
// the defaults.
type accessLogOptions struct {
	maxNodes          int
	sessionGapSeconds int
}

// logRecord is one usable request observation.
type logRecord struct {
	time   time.Time
	client string // IP + user agent
	method string
	path   string // raw path (with query)
}

func fromAccessLog(data []byte, opts accessLogOptions) (scenariofile.Scenario, AccessLogStats, error) {
	var stats AccessLogStats
	var records []logRecord

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		rec, ok := parseLogLine(line)
		if !ok || !keepRequest(rec.method, rec.path) {
			stats.Skipped++
			continue
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		return scenariofile.Scenario{}, stats, fmt.Errorf("importer: read access log: %w", err)
	}
	if len(records) == 0 {
		return scenariofile.Scenario{}, stats, fmt.Errorf("importer: access log has no usable requests")
	}
	stats.Requests = len(records)

	sessions, clients := sessionize(records, time.Duration(opts.sessionGapSeconds)*time.Second)
	stats.Sessions = len(sessions)
	stats.Clients = clients

	// Collapse raw paths into endpoints and keep the hottest maxNodes of them;
	// requests to folded endpoints are removed from each session so the
	// surviving transitions bridge across them (the standard reduction for an
	// over-wide transition model).
	kept, dropped := capEndpoints(records, opts.maxNodes)
	stats.DroppedEndpoints = dropped

	g := buildLearnedGraph(sessions, kept)

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

// combinedRE matches the Apache/nginx common and combined log formats:
//
//	host ident user [time] "METHOD path proto" status bytes ["referer" "ua"]
var combinedRE = regexp.MustCompile(
	`^(\S+) \S+ \S+ \[([^\]]+)\] "([A-Za-z]+) (\S+)[^"]*" (\d{3}) \S+(?: "[^"]*" "([^"]*)")?`)

const combinedTimeLayout = "02/Jan/2006:15:04:05 -0700"

// parseLogLine parses one access-log line in either supported shape: the
// combined text format, or a JSON object (one per line) with tolerant key
// names. It returns ok=false for anything else.
func parseLogLine(line string) (logRecord, bool) {
	if strings.HasPrefix(line, "{") {
		return parseJSONLine(line)
	}
	m := combinedRE.FindStringSubmatch(line)
	if m == nil {
		return logRecord{}, false
	}
	t, err := time.Parse(combinedTimeLayout, m[2])
	if err != nil {
		return logRecord{}, false
	}
	return logRecord{
		time:   t,
		client: m[1] + "\x00" + m[6],
		method: strings.ToUpper(m[3]),
		path:   m[4],
	}, true
}

// jsonLogLine accepts the common key spellings across nginx/envoy/app loggers,
// so a JSON access log works without a mapping config. Either split
// method/path keys or a combined "request" line is fine.
type jsonLogLine struct {
	Time      json.RawMessage `json:"time"`
	Ts        json.RawMessage `json:"ts"`
	Timestamp json.RawMessage `json:"timestamp"`
	AtTime    json.RawMessage `json:"@timestamp"`

	Method        string `json:"method"`
	RequestMethod string `json:"request_method"`
	Path          string `json:"path"`
	URI           string `json:"uri"`
	RequestURI    string `json:"request_uri"`
	URL           string `json:"url"`
	Request       string `json:"request"` // "GET /path HTTP/1.1"

	RemoteAddr string `json:"remote_addr"`
	ClientIP   string `json:"client_ip"`
	IP         string `json:"ip"`

	UserAgent     string `json:"user_agent"`
	HTTPUserAgent string `json:"http_user_agent"`
	UA            string `json:"ua"`
}

func parseJSONLine(line string) (logRecord, bool) {
	var j jsonLogLine
	if err := json.Unmarshal([]byte(line), &j); err != nil {
		return logRecord{}, false
	}

	t, ok := parseJSONTime(firstRaw(j.Time, j.Ts, j.Timestamp, j.AtTime))
	if !ok {
		return logRecord{}, false
	}

	method := firstNonEmpty(j.Method, j.RequestMethod)
	path := firstNonEmpty(j.Path, j.URI, j.RequestURI, j.URL)
	if (method == "" || path == "") && j.Request != "" {
		fields := strings.Fields(j.Request)
		if len(fields) >= 2 {
			method, path = fields[0], fields[1]
		}
	}
	if method == "" || path == "" || !strings.HasPrefix(path, "/") {
		return logRecord{}, false
	}

	ip := firstNonEmpty(j.RemoteAddr, j.ClientIP, j.IP)
	ua := firstNonEmpty(j.UserAgent, j.HTTPUserAgent, j.UA)
	return logRecord{
		time:   t,
		client: ip + "\x00" + ua,
		method: strings.ToUpper(method),
		path:   path,
	}, true
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
}

// buildLearnedGraph turns the sessions into a weighted behavior graph over the
// kept endpoints: transition counts normalize into per-node edge weights, each
// session's last request earns an edge into the terminal exit node, the most
// common session start becomes the start node, and maxSteps tracks the p95
// session length.
func buildLearnedGraph(sessions [][]logRecord, kept map[string]bool) learnedGraph {
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
	for _, key := range endpointOrder {
		method, pattern, _ := strings.Cut(key, " ")
		id := ids.unique(sanitize(strings.ToLower(method) + "_" + pattern))
		nodeID[key] = id
		tmplID := domain.ID("t_" + id)
		nodes = append(nodes, domain.Node{ID: domain.ID(id), APITemplateID: tmplID})
		templates[tmplID] = domain.APITemplate{
			ID:       tmplID,
			Protocol: domain.ProtocolREST,
			Method:   method,
			Path:     mostObserved(concrete[key]),
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

	return learnedGraph{
		graph:     domain.ScenarioGraph{ID: "learned", Nodes: nodes, Edges: edges},
		templates: templates,
		start:     nodeID[start],
		maxSteps:  clamp(percentileInt(lengths, 0.95), 4, 100),
	}
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
