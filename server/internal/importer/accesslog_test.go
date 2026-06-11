package importer

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
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
	sc, stats, err := fromAccessLog([]byte(b.String()), accessLogOptions{maxNodes: 2, sessionGapSeconds: 1800})
	if err != nil {
		t.Fatalf("fromAccessLog: %v", err)
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
