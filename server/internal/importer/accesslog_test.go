package importer

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// combinedLog is a small synthetic capture in Apache/nginx combined format:
// three clients walking a branching shop journey. Client 1.1.1.1 browses,
// searches, views two different products (numeric ids that must collapse into
// one endpoint) and adds to cart; client 2.2.2.2 browses then leaves via the
// category page; client 3.3.3.3 has two sessions split by an idle gap. An
// asset request and a malformed line are planted to be skipped.
const combinedLog = `1.1.1.1 - - [10/Jun/2026:10:00:00 +0000] "GET /browse HTTP/1.1" 200 512 "-" "ua-a"
1.1.1.1 - - [10/Jun/2026:10:00:05 +0000] "GET /search?q=shoes HTTP/1.1" 200 900 "-" "ua-a"
1.1.1.1 - - [10/Jun/2026:10:00:09 +0000] "GET /product/123 HTTP/1.1" 200 700 "-" "ua-a"
1.1.1.1 - - [10/Jun/2026:10:00:15 +0000] "GET /product/456 HTTP/1.1" 404 120 "-" "ua-a"
1.1.1.1 - - [10/Jun/2026:10:00:21 +0000] "POST /cart HTTP/1.1" 200 64 "-" "ua-a"
2.2.2.2 - - [10/Jun/2026:10:00:02 +0000] "GET /browse HTTP/1.1" 200 512 "-" "ua-b"
2.2.2.2 - - [10/Jun/2026:10:00:08 +0000] "GET /category HTTP/1.1" 200 512 "-" "ua-b"
2.2.2.2 - - [10/Jun/2026:10:00:10 +0000] "GET /static/app.css HTTP/1.1" 200 100 "-" "ua-b"
3.3.3.3 - - [10/Jun/2026:10:00:03 +0000] "GET /browse HTTP/1.1" 200 512 "-" "ua-c"
3.3.3.3 - - [10/Jun/2026:10:00:07 +0000] "GET /search?q=hat HTTP/1.1" 200 900 "-" "ua-c"
not a log line at all
3.3.3.3 - - [10/Jun/2026:11:00:00 +0000] "GET /browse HTTP/1.1" 200 512 "-" "ua-c"
3.3.3.3 - - [10/Jun/2026:11:00:04 +0000] "GET /product/123 HTTP/1.1" 200 700 "-" "ua-c"
`

func learnedNode(t *testing.T, sc scenariofile.Scenario, id string) domain.Node {
	t.Helper()
	for _, n := range sc.Graph.Nodes {
		if n.ID == domain.ID(id) {
			return n
		}
	}
	t.Fatalf("node %q missing from learned graph (have %v)", id, sc.Graph.Nodes)
	return domain.Node{}
}

func learnedEdge(sc scenariofile.Scenario, from, to string) (domain.Edge, bool) {
	for _, e := range sc.Graph.Edges {
		if e.From == domain.ID(from) && e.To == domain.ID(to) {
			return e, true
		}
	}
	return domain.Edge{}, false
}

func TestFromAccessLogLearnsBranchingGraph(t *testing.T) {
	sc, stats, err := FromAccessLog([]byte(combinedLog))
	if err != nil {
		t.Fatalf("FromAccessLog: %v", err)
	}

	// The two numeric product paths collapse into one endpoint node.
	learnedNode(t, sc, "get_product_id")
	if _, found := learnedEdge(sc, "get_product_id", "get_product_id"); !found {
		t.Error("expected a self-edge on get_product_id (client 1 viewed two products in a row)")
	}

	// Every session starts at /browse, so it is the start node.
	if sc.Start != "get_browse" {
		t.Errorf("start = %q, want get_browse", sc.Start)
	}

	// browse branches: of its 4 outgoing transitions, 2 went to search, 1 to
	// category, 1 to product. Weights are per-node proportions.
	if e, ok := learnedEdge(sc, "get_browse", "get_search"); !ok || e.Weight < 0.45 || e.Weight > 0.55 {
		t.Errorf("browse->search = %+v, want weight ~0.5", e)
	}
	if e, ok := learnedEdge(sc, "get_browse", "get_category"); !ok || e.Weight < 0.2 || e.Weight > 0.3 {
		t.Errorf("browse->category = %+v, want weight ~0.25", e)
	}

	// Session ends become exit edges (cart, category, search, product each
	// ended a session at least once).
	if _, ok := learnedEdge(sc, "post_cart", "exit"); !ok {
		t.Error("post_cart should have an exit edge (client 1's session ended there)")
	}
	exitNode := learnedNode(t, sc, "exit")
	if exitNode.APITemplateID != "" {
		t.Error("exit must be a terminal node (no template)")
	}

	// Templates: keyed t_<node>, method+path from the most observed concrete
	// request, so the scenario is runnable as-is.
	tmpl, ok := sc.Templates["t_get_product_id"]
	if !ok {
		t.Fatalf("t_get_product_id template missing (have %v)", sc.Templates)
	}
	if tmpl.Method != "GET" || !strings.HasPrefix(tmpl.Path, "/product/") || strings.Contains(tmpl.Path, "{") {
		t.Errorf("t_get_product_id = %+v, want a concrete observed path like /product/123", tmpl)
	}
	if got := sc.Templates["t_get_search"].Path; !strings.HasPrefix(got, "/search?q=") {
		t.Errorf("search template path = %q, want an observed path with its query", got)
	}

	// Stats: 11 usable requests (12 hits minus the asset), the malformed line
	// and the asset are skipped, 4 sessions from 3 clients (3.3.3.3 splits on
	// the one-hour gap).
	if stats.Requests != 11 {
		t.Errorf("stats.Requests = %d, want 11", stats.Requests)
	}
	if stats.Skipped != 2 {
		t.Errorf("stats.Skipped = %d, want 2 (asset + malformed line)", stats.Skipped)
	}
	if stats.Sessions != 4 {
		t.Errorf("stats.Sessions = %d, want 4 (gap splits client 3)", stats.Sessions)
	}
	if stats.Clients != 3 {
		t.Errorf("stats.Clients = %d, want 3", stats.Clients)
	}

	// The learned document is the graph-first form: with a target it expands
	// into a valid RunSpec (strict scenario validation included).
	sc.Target = "http://localhost:9000"
	spec, err := scenariofile.Expand(sc)
	if err != nil {
		t.Fatalf("Expand(learned): %v", err)
	}
	if spec.Workload == nil || spec.Workload.Kind != domain.WorkloadOpen {
		t.Errorf("learned scenario should suggest an open workload, got %+v", spec.Workload)
	}
	if sc.MaxSteps < 4 {
		t.Errorf("maxSteps = %d, want >= 4", sc.MaxSteps)
	}
}

func TestFromAccessLogParsesJSONLines(t *testing.T) {
	// Two JSON shapes: split method/path keys with an RFC3339 time, and an
	// nginx-style combined "request" line with a unix-seconds timestamp.
	const jsonl = `{"time":"2026-06-10T10:00:00Z","remote_addr":"1.1.1.1","method":"GET","path":"/a","status":200,"user_agent":"ua"}
{"ts":1781086805,"remote_addr":"1.1.1.1","request":"GET /b HTTP/1.1","status":200,"http_user_agent":"ua"}
`
	sc, stats, err := FromAccessLog([]byte(jsonl))
	if err != nil {
		t.Fatalf("FromAccessLog(jsonl): %v", err)
	}
	if stats.Requests != 2 {
		t.Fatalf("stats.Requests = %d, want 2", stats.Requests)
	}
	if _, ok := learnedEdge(sc, "get_a", "get_b"); !ok {
		t.Errorf("expected the a->b transition from one client session; edges = %v", sc.Graph.Edges)
	}
}

func TestFromAccessLogCapsEndpoints(t *testing.T) {
	// One client walks hub -> spoke_i -> hub ...; with MaxNodes=2 only the two
	// hottest endpoints stay, the spokes fold out, and their transitions
	// bridge (hub -> hub).
	var b strings.Builder
	times := []string{"10:00:00", "10:00:02", "10:00:04", "10:00:06", "10:00:08", "10:00:10", "10:00:12"}
	paths := []string{"/hub", "/spoke1", "/hub", "/spoke2", "/hub", "/top", "/top"}
	for i, p := range paths {
		b.WriteString(`9.9.9.9 - - [10/Jun/2026:` + times[i] + ` +0000] "GET ` + p + ` HTTP/1.1" 200 1 "-" "ua"` + "\n")
	}
	sc, stats, err := FromAccessLogWithOptions([]byte(b.String()), AccessLogOptions{MaxNodes: 2})
	if err != nil {
		t.Fatalf("FromAccessLogWithOptions: %v", err)
	}
	if stats.DroppedEndpoints != 2 {
		t.Errorf("DroppedEndpoints = %d, want 2 (spoke1, spoke2)", stats.DroppedEndpoints)
	}
	if _, ok := learnedEdge(sc, "get_hub", "get_hub"); !ok {
		t.Errorf("expected hub->hub bridge across a dropped spoke; edges = %v", sc.Graph.Edges)
	}
	for _, n := range sc.Graph.Nodes {
		if strings.Contains(string(n.ID), "spoke") {
			t.Errorf("dropped endpoint %q still in graph", n.ID)
		}
	}
}

func TestFromAccessLogRejectsEmpty(t *testing.T) {
	if _, _, err := FromAccessLog([]byte("nonsense\nlines only\n")); err == nil {
		t.Error("expected an error when no line parses")
	}
}

func TestFromAccessLogSamplesSkippedLines(t *testing.T) {
	// Eleven garbage lines around two good ones: the skip count covers them
	// all, and the first ten parse failures are sampled with line number, a
	// 120-char text prefix and a reason — so a half-broken real-world log
	// reports *why* coverage is partial instead of a bare count. The asset
	// request on line 2 is filtered by design, so it is counted but never
	// sampled as a failure.
	var b strings.Builder
	b.WriteString(`1.1.1.1 - - [10/Jun/2026:10:00:00 +0000] "GET /a HTTP/1.1" 200 1 "-" "ua"` + "\n")
	b.WriteString(`1.1.1.1 - - [10/Jun/2026:10:00:01 +0000] "GET /app.css HTTP/1.1" 200 1 "-" "ua"` + "\n")
	long := "garbage-" + strings.Repeat("x", 200)
	for i := 0; i < 11; i++ {
		b.WriteString(long + "\n")
	}
	b.WriteString(`1.1.1.1 - - [10/Jun/2026:10:00:02 +0000] "GET /b HTTP/1.1" 200 1 "-" "ua"` + "\n")

	_, stats, err := FromAccessLog([]byte(b.String()))
	if err != nil {
		t.Fatalf("FromAccessLog: %v", err)
	}
	if stats.Requests != 2 {
		t.Errorf("stats.Requests = %d, want 2", stats.Requests)
	}
	if stats.Skipped != 12 {
		t.Errorf("stats.Skipped = %d, want 12 (asset + 11 garbage)", stats.Skipped)
	}
	if len(stats.SkippedSamples) != 10 {
		t.Fatalf("len(SkippedSamples) = %d, want 10 (capped)", len(stats.SkippedSamples))
	}
	first := stats.SkippedSamples[0]
	if first.Line != 3 {
		t.Errorf("first sample line = %d, want 3 (the asset on line 2 is filtered, not failed)", first.Line)
	}
	if got := len([]rune(first.Text)); got != 120 {
		t.Errorf("sample text length = %d runes, want 120 (truncated)", got)
	}
	if !strings.HasPrefix(first.Text, "garbage-") {
		t.Errorf("sample text = %q, want the raw line prefix", first.Text)
	}
	if first.Reason == "" {
		t.Error("sample reason must not be empty")
	}
	for _, s := range stats.SkippedSamples {
		if s.Line == 2 {
			t.Errorf("asset line 2 must not be sampled as a parse failure: %+v", s)
		}
	}
}

func TestFromAccessLogSessionGapOption(t *testing.T) {
	// Two requests 100s apart: one session under the default 1800s gap, two
	// when the caller tightens the gap below the pause.
	log := `1.1.1.1 - - [10/Jun/2026:10:00:00 +0000] "GET /a HTTP/1.1" 200 1 "-" "ua"
1.1.1.1 - - [10/Jun/2026:10:01:40 +0000] "GET /b HTTP/1.1" 200 1 "-" "ua"
`
	_, stats, err := FromAccessLog([]byte(log))
	if err != nil {
		t.Fatalf("FromAccessLog: %v", err)
	}
	if stats.Sessions != 1 {
		t.Errorf("default gap: sessions = %d, want 1", stats.Sessions)
	}
	_, stats, err = FromAccessLogWithOptions([]byte(log), AccessLogOptions{SessionGapSeconds: 50})
	if err != nil {
		t.Fatalf("FromAccessLogWithOptions: %v", err)
	}
	if stats.Sessions != 2 {
		t.Errorf("50s gap: sessions = %d, want 2", stats.Sessions)
	}
}

func TestFromAccessLogPromotesVariables(t *testing.T) {
	// Three clients hit /product/{id} with three distinct ids (101 hottest)
	// and one walks into the nested /product/{id}/reviews. With promotion on,
	// the collapsed segment becomes a template variable in the existing
	// {{.var}} representation (load.Render's variable system) instead of
	// pinning every session to the single most-observed resource, and the
	// observed values are reported as a sample pool capped at the option.
	log := `1.1.1.1 - - [10/Jun/2026:10:00:00 +0000] "GET /browse HTTP/1.1" 200 1 "-" "ua-a"
1.1.1.1 - - [10/Jun/2026:10:00:04 +0000] "GET /product/101 HTTP/1.1" 200 1 "-" "ua-a"
1.1.1.1 - - [10/Jun/2026:10:00:08 +0000] "GET /product/101/reviews HTTP/1.1" 200 1 "-" "ua-a"
2.2.2.2 - - [10/Jun/2026:10:00:01 +0000] "GET /browse HTTP/1.1" 200 1 "-" "ua-b"
2.2.2.2 - - [10/Jun/2026:10:00:05 +0000] "GET /product/202 HTTP/1.1" 200 1 "-" "ua-b"
3.3.3.3 - - [10/Jun/2026:10:00:02 +0000] "GET /product/101 HTTP/1.1" 200 1 "-" "ua-c"
4.4.4.4 - - [10/Jun/2026:10:00:03 +0000] "GET /product/303 HTTP/1.1" 200 1 "-" "ua-d"
`
	sc, stats, err := FromAccessLogWithOptions([]byte(log), AccessLogOptions{
		PromoteVariables:   true,
		MaxVariableSamples: 2,
	})
	if err != nil {
		t.Fatalf("FromAccessLogWithOptions: %v", err)
	}

	if got := sc.Templates["t_get_product_id"].Path; got != "/product/{{.product_id}}" {
		t.Errorf("product template path = %q, want /product/{{.product_id}}", got)
	}
	if got := sc.Templates["t_get_product_id_reviews"].Path; got != "/product/{{.product_id}}/reviews" {
		t.Errorf("reviews template path = %q, want /product/{{.product_id}}/reviews", got)
	}
	// Non-collapsed endpoints stay concrete.
	if got := sc.Templates["t_get_browse"].Path; got != "/browse" {
		t.Errorf("browse template path = %q, want /browse", got)
	}

	// Both templates share one pool under the product_id name; the pool keeps
	// the hottest MaxVariableSamples values (101 observed 3x across both
	// endpoints, then 202/303 once each — the lexicographic tie-break keeps
	// 202).
	if len(stats.Variables) != 1 {
		t.Fatalf("stats.Variables = %+v, want exactly product_id", stats.Variables)
	}
	v := stats.Variables[0]
	if v.Name != "product_id" {
		t.Errorf("variable name = %q, want product_id", v.Name)
	}
	if len(v.Values) != 2 || v.Values[0] != "101" || v.Values[1] != "202" {
		t.Errorf("variable values = %v, want [101 202]", v.Values)
	}

	// The promoted path is consistent with the load runtime's variable system:
	// a value from the pool renders into a concrete request URL.
	req, err := load.Render(sc.Templates["t_get_product_id"], "http://localhost:9000", domain.Credential{}, map[string]string{"product_id": v.Values[1]})
	if err != nil {
		t.Fatalf("Render(promoted template): %v", err)
	}
	if req.URL != "http://localhost:9000/product/202" {
		t.Errorf("rendered URL = %q, want the pooled value substituted", req.URL)
	}

	// Promotion is opt-in: the default path stays concrete and pool-free.
	scDefault, statsDefault, err := FromAccessLog([]byte(log))
	if err != nil {
		t.Fatalf("FromAccessLog: %v", err)
	}
	if got := scDefault.Templates["t_get_product_id"].Path; strings.Contains(got, "{") {
		t.Errorf("default product path = %q, want a concrete observed path", got)
	}
	if len(statsDefault.Variables) != 0 {
		t.Errorf("default stats.Variables = %+v, want none", statsDefault.Variables)
	}
}
