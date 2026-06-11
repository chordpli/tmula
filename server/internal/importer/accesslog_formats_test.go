package importer

import (
	"strings"
	"testing"
)

// albLog is adapted from the documented example entries of an Application Load
// Balancer access log (space-delimited; the request line and user agent are
// double-quoted fields):
// https://docs.aws.amazon.com/elasticloadbalancing/latest/application/load-balancer-access-logs.html
// One client walks browse -> product -> cart; a truncated line is planted to
// be skipped.
const albLog = `http 2018-07-02T22:23:00.186641Z app/my-loadbalancer/50dc6c495c0c9188 192.168.131.39:2817 10.0.0.1:80 0.000 0.001 0.000 200 200 34 366 "GET http://www.example.com:80/browse HTTP/1.1" "curl/7.46.0" - - arn:aws:elasticloadbalancing:us-east-2:123456789012:targetgroup/my-targets/73e2d6bc24d8a067 "Root=1-58337262-36d228ad5d99923122bbe354" "-" "-" 0 2018-07-02T22:22:48.364000Z "forward" "-" "-" "10.0.0.1:80" "200" "-" "-" TID_1234abcd5678ef90 "-" "-" "-"
http 2018-07-02T22:23:05.186641Z app/my-loadbalancer/50dc6c495c0c9188 192.168.131.39:2817 10.0.0.1:80 0.000 0.001 0.000 200 200 34 366 "GET http://www.example.com:80/product/42 HTTP/1.1" "curl/7.46.0" - - arn:aws:elasticloadbalancing:us-east-2:123456789012:targetgroup/my-targets/73e2d6bc24d8a067 "Root=1-58337262-36d228ad5d99923122bbe355" "-" "-" 0 2018-07-02T22:22:53.364000Z "forward" "-" "-" "10.0.0.1:80" "200" "-" "-" TID_1234abcd5678ef90 "-" "-" "-"
https 2018-07-02T22:23:09.186641Z app/my-loadbalancer/50dc6c495c0c9188 192.168.131.39:2817 10.0.0.1:80 0.086 0.048 0.037 200 200 0 57 "POST https://www.example.com:443/cart HTTP/1.1" "curl/7.46.0" ECDHE-RSA-AES128-GCM-SHA256 TLSv1.2 arn:aws:elasticloadbalancing:us-east-2:123456789012:targetgroup/my-targets/73e2d6bc24d8a067 "Root=1-58337281-1d84f3d73c47ec4e58577259" "www.example.com" "-" 1 2018-07-02T22:22:57.364000Z "forward" "-" "-" "10.0.0.1:80" "200" "-" "-" TID_1234abcd5678ef90 "-" "-" "-"
http 2018-07-02T22:23:1
`

func TestFromAccessLogParsesALB(t *testing.T) {
	sc, stats, err := FromAccessLog([]byte(albLog))
	if err != nil {
		t.Fatalf("FromAccessLog(alb): %v", err)
	}
	if stats.Format != FormatALB {
		t.Errorf("stats.Format = %q, want %q", stats.Format, FormatALB)
	}
	if stats.Requests != 3 {
		t.Errorf("stats.Requests = %d, want 3", stats.Requests)
	}
	if stats.Skipped != 1 {
		t.Errorf("stats.Skipped = %d, want 1 (the truncated line)", stats.Skipped)
	}
	// All three requests come from one client (same IP + UA), in one session.
	if stats.Clients != 1 || stats.Sessions != 1 {
		t.Errorf("clients/sessions = %d/%d, want 1/1", stats.Clients, stats.Sessions)
	}
	// The numeric product path collapses; the journey edges follow the walk.
	learnedNode(t, sc, "get_product_id")
	if _, ok := learnedEdge(sc, "get_browse", "get_product_id"); !ok {
		t.Errorf("expected browse->product edge; edges = %v", sc.Graph.Edges)
	}
	if _, ok := learnedEdge(sc, "get_product_id", "post_cart"); !ok {
		t.Errorf("expected product->cart edge; edges = %v", sc.Graph.Edges)
	}
	// The path comes from the request-line URL with host and scheme stripped.
	if got := sc.Templates["t_get_browse"].Path; got != "/browse" {
		t.Errorf("browse template path = %q, want /browse", got)
	}
}

// cloudFrontLog mirrors the documented standard (legacy) log file format:
// tab-separated columns under #Version/#Fields directives, cs-uri-stem without
// the query and cs-uri-query as "-" when absent, URL-encoded user agent:
// https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/standard-logs-reference.html
func cloudFrontLog() string {
	rows := [][]string{
		{"2019-12-04", "21:02:31", "LAX1", "392", "192.0.2.100", "GET", "d111111abcdef8.cloudfront.net", "/browse", "200", "-", "Mozilla/5.0%20(Windows)", "-"},
		{"2019-12-04", "21:02:35", "LAX1", "392", "192.0.2.100", "GET", "d111111abcdef8.cloudfront.net", "/search", "200", "-", "Mozilla/5.0%20(Windows)", "q=shoes"},
		{"2019-12-04", "21:02:39", "LAX1", "392", "192.0.2.100", "GET", "d111111abcdef8.cloudfront.net", "/product/9", "200", "-", "Mozilla/5.0%20(Windows)", "-"},
	}
	var b strings.Builder
	b.WriteString("#Version: 1.0\n")
	b.WriteString("#Fields: date time x-edge-location sc-bytes c-ip cs-method cs(Host) cs-uri-stem sc-status cs(Referer) cs(User-Agent) cs-uri-query\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t") + "\n")
	}
	return b.String()
}

func TestFromAccessLogParsesCloudFront(t *testing.T) {
	sc, stats, err := FromAccessLog([]byte(cloudFrontLog()))
	if err != nil {
		t.Fatalf("FromAccessLog(cloudfront): %v", err)
	}
	if stats.Format != FormatCloudFront {
		t.Errorf("stats.Format = %q, want %q", stats.Format, FormatCloudFront)
	}
	if stats.Requests != 3 {
		t.Errorf("stats.Requests = %d, want 3", stats.Requests)
	}
	// The #Version/#Fields directives are structure, not data: not "skipped".
	if stats.Skipped != 0 {
		t.Errorf("stats.Skipped = %d, want 0 (directives are not data lines)", stats.Skipped)
	}
	// cs-uri-query reattaches to the stem; "-" means no query.
	if got := sc.Templates["t_get_search"].Path; got != "/search?q=shoes" {
		t.Errorf("search template path = %q, want /search?q=shoes", got)
	}
	learnedNode(t, sc, "get_product_id")
	if _, ok := learnedEdge(sc, "get_browse", "get_search"); !ok {
		t.Errorf("expected browse->search edge; edges = %v", sc.Graph.Edges)
	}
}

func TestFromAccessLogRequiresCloudFrontFieldsHeader(t *testing.T) {
	// A beheaded CloudFront log (grep stripped the directives) cannot be parsed
	// because the column order is unknown; the diagnostics must say so.
	data := "2019-12-04\t21:02:31\tLAX1\t392\t192.0.2.100\tGET\thost\t/browse\t200\t-\tua\t-\n"
	_, stats, err := FromAccessLogWithOptions([]byte(data), AccessLogOptions{Format: FormatCloudFront})
	if err == nil {
		t.Fatal("expected an error for a CloudFront log without a #Fields header")
	}
	if len(stats.SkippedSamples) == 0 || !strings.Contains(stats.SkippedSamples[0].Reason, "#Fields") {
		t.Errorf("skipped samples = %+v, want a reason mentioning the #Fields header", stats.SkippedSamples)
	}
}

// caddyLog follows Caddy's structured access log shape: ts as unix-seconds
// float and the request fields nested under "request" (remote_ip/client_ip,
// method, uri, headers): https://caddyserver.com/docs/logging#structured-logs
const caddyLog = `{"level":"info","ts":1646861401.52,"logger":"http.log.access","msg":"handled request","request":{"remote_ip":"127.0.0.1","remote_port":"41342","client_ip":"127.0.0.1","proto":"HTTP/2.0","method":"GET","host":"localhost","uri":"/browse","headers":{"User-Agent":["curl/7.82.0"]}},"duration":0.000929675,"size":10900,"status":200}
{"level":"info","ts":1646861405.52,"logger":"http.log.access","msg":"handled request","request":{"remote_ip":"127.0.0.1","remote_port":"41342","client_ip":"127.0.0.1","proto":"HTTP/2.0","method":"GET","host":"localhost","uri":"/product/123","headers":{"User-Agent":["curl/7.82.0"]}},"duration":0.000929675,"size":10900,"status":200}
`

func TestFromAccessLogParsesCaddyJSON(t *testing.T) {
	sc, stats, err := FromAccessLog([]byte(caddyLog))
	if err != nil {
		t.Fatalf("FromAccessLog(caddy): %v", err)
	}
	if stats.Format != FormatCaddy {
		t.Errorf("stats.Format = %q, want %q", stats.Format, FormatCaddy)
	}
	if stats.Requests != 2 || stats.Clients != 1 {
		t.Errorf("requests/clients = %d/%d, want 2/1", stats.Requests, stats.Clients)
	}
	if _, ok := learnedEdge(sc, "get_browse", "get_product_id"); !ok {
		t.Errorf("expected browse->product edge; edges = %v", sc.Graph.Edges)
	}
}

// traefikLog follows Traefik's JSON access log shape: StartUTC timestamp,
// ClientHost, RequestMethod/RequestPath, and request headers flattened with a
// request_ prefix (request_User-Agent):
// https://doc.traefik.io/traefik/reference/install-configuration/observability/logs-and-accesslogs/
const traefikLog = `{"ClientAddr":"10.9.129.184:47134","ClientHost":"10.9.129.184","ClientPort":"47134","DownstreamStatus":200,"Duration":80879,"RequestMethod":"GET","RequestPath":"/browse","RequestProtocol":"HTTP/1.1","RouterName":"web@docker","StartUTC":"2022-11-02T00:22:41.640464109Z","entryPointName":"web","level":"info","msg":"","request_User-Agent":"kube-probe/1.23","time":"2022-11-02T00:22:41Z"}
{"ClientAddr":"10.9.129.184:47134","ClientHost":"10.9.129.184","ClientPort":"47134","DownstreamStatus":200,"Duration":80879,"RequestMethod":"POST","RequestPath":"/cart","RequestProtocol":"HTTP/1.1","RouterName":"web@docker","StartUTC":"2022-11-02T00:22:45.640464109Z","entryPointName":"web","level":"info","msg":"","request_User-Agent":"kube-probe/1.23","time":"2022-11-02T00:22:45Z"}
`

func TestFromAccessLogParsesTraefikJSON(t *testing.T) {
	sc, stats, err := FromAccessLog([]byte(traefikLog))
	if err != nil {
		t.Fatalf("FromAccessLog(traefik): %v", err)
	}
	if stats.Format != FormatTraefik {
		t.Errorf("stats.Format = %q, want %q", stats.Format, FormatTraefik)
	}
	if stats.Requests != 2 || stats.Clients != 1 {
		t.Errorf("requests/clients = %d/%d, want 2/1", stats.Requests, stats.Clients)
	}
	if _, ok := learnedEdge(sc, "get_browse", "post_cart"); !ok {
		t.Errorf("expected browse->cart edge; edges = %v", sc.Graph.Edges)
	}
}

func TestDetectAccessLogFormat(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
		ok   bool
	}{
		{"combined", combinedLog, FormatCombined, true},
		{"json lines", `{"time":"2026-06-10T10:00:00Z","method":"GET","path":"/a","remote_addr":"1.1.1.1"}` + "\n", FormatJSONLines, true},
		{"alb", albLog, FormatALB, true},
		{"cloudfront", cloudFrontLog(), FormatCloudFront, true},
		{"caddy", caddyLog, FormatCaddy, true},
		{"traefik", traefikLog, FormatTraefik, true},
		// A rotated file may open with a truncated line; detection reads on.
		{"truncated first line", "06:10:00 +0000] \"GET /a HTTP/1.1\" 200 1\n" + combinedLog, FormatCombined, true},
		{"openapi yaml", "openapi: 3.0.0\npaths:\n  /a: {}\n", "", false},
		{"pretty-printed json doc", "{\n  \"log\": {\"entries\": []}\n}\n", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		got, ok := DetectAccessLogFormat([]byte(c.data))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: DetectAccessLogFormat = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
		if looks := LooksLikeAccessLog([]byte(c.data)); looks != c.ok {
			t.Errorf("%s: LooksLikeAccessLog = %v, want %v", c.name, looks, c.ok)
		}
	}
}

func TestFromAccessLogFormatHint(t *testing.T) {
	// An unknown hint fails fast instead of silently skipping every line.
	if _, _, err := FromAccessLogWithOptions([]byte(combinedLog), AccessLogOptions{Format: "nginx"}); err == nil || !strings.Contains(err.Error(), "unknown access log format") {
		t.Errorf("unknown hint: err = %v, want an unknown-format error", err)
	}
	// An explicit hint overrides detection: combined lines forced through the
	// JSON parser are all unusable.
	if _, _, err := FromAccessLogWithOptions([]byte(combinedLog), AccessLogOptions{Format: FormatJSONLines}); err == nil {
		t.Error("expected an error when a combined log is forced through the json parser")
	}
	// The caddy/traefik hints route to the JSON-lines parser.
	_, stats, err := FromAccessLogWithOptions([]byte(caddyLog), AccessLogOptions{Format: FormatCaddy})
	if err != nil {
		t.Fatalf("FromAccessLogWithOptions(caddy hint): %v", err)
	}
	if stats.Format != FormatCaddy || stats.Requests != 2 {
		t.Errorf("caddy hint: format/requests = %q/%d, want %q/2", stats.Format, stats.Requests, FormatCaddy)
	}
}
