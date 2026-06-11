package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chordpli/tmula/server/internal/scenariofile"
)

func TestInitFromOpenAPIWritesRunnableScenario(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "api.yaml")
	openapi := "openapi: 3.0.0\n" +
		"servers:\n  - url: http://svc.test\n" +
		"paths:\n  /ping:\n    get: {}\n  /echo:\n    post:\n      operationId: echo\n"
	if err := os.WriteFile(in, []byte(openapi), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	out := filepath.Join(dir, "scenario.yaml")

	if err := initScenario([]string{"--from", in, "--out", out}); err != nil {
		t.Fatalf("initScenario: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	sc, err := scenariofile.Parse(data)
	if err != nil {
		t.Fatalf("parse generated scenario: %v", err)
	}
	if sc.Target != "http://svc.test" || len(sc.Flow) != 2 {
		t.Errorf("generated scenario target=%q flow=%d, want http://svc.test / 2", sc.Target, len(sc.Flow))
	}
	if _, err := scenariofile.Expand(sc); err != nil {
		t.Errorf("generated scenario does not expand: %v", err)
	}
}

func TestInitRequiresFrom(t *testing.T) {
	if err := initScenario([]string{}); err == nil {
		t.Error("init without --from should error")
	}
}

// sampleAccessLog is a tiny combined-format capture: one client browsing then
// buying, one client browsing then leaving.
const sampleAccessLog = `1.1.1.1 - - [10/Jun/2026:10:00:00 +0000] "GET /browse HTTP/1.1" 200 512 "-" "ua-a"
1.1.1.1 - - [10/Jun/2026:10:00:05 +0000] "POST /cart HTTP/1.1" 200 64 "-" "ua-a"
2.2.2.2 - - [10/Jun/2026:10:00:02 +0000] "GET /browse HTTP/1.1" 200 512 "-" "ua-b"
`

func TestInitFromAccessLogWritesGraphFirstScenario(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "access.log")
	if err := os.WriteFile(in, []byte(sampleAccessLog), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	out := filepath.Join(dir, "scenario.yaml")

	// Logs carry no host, so init must demand a target...
	if err := initScenario([]string{"--from", in, "--out", out}); err == nil {
		t.Error("init from an access log without --target should error")
	}
	// ...and produce a graph-first scenario when given one.
	if err := initScenario([]string{"--from", in, "--out", out, "--target", "http://svc.test"}); err != nil {
		t.Fatalf("initScenario: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	sc, err := scenariofile.Parse(data)
	if err != nil {
		t.Fatalf("parse generated scenario: %v", err)
	}
	if sc.Graph == nil || len(sc.Graph.Nodes) < 3 || sc.Start == "" {
		t.Fatalf("generated scenario should be graph-first with a start; graph=%+v start=%q", sc.Graph, sc.Start)
	}
	if _, err := scenariofile.Expand(sc); err != nil {
		t.Errorf("generated scenario does not expand: %v", err)
	}
}

func TestImportRunSpecLearnsFromAccessLogWithoutTarget(t *testing.T) {
	// The web import endpoint only consumes graph/templates/start/maxSteps, so
	// a log (which names no host) must still convert instead of failing on the
	// missing target.
	spec, err := importRunSpec([]byte(sampleAccessLog), "auto")
	if err != nil {
		t.Fatalf("importRunSpec: %v", err)
	}
	if len(spec.Graph.Nodes) < 3 || spec.Start == "" {
		t.Errorf("learned spec graph=%d nodes start=%q, want a populated graph", len(spec.Graph.Nodes), spec.Start)
	}
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		format, name string
		data, want   string
	}{
		{"auto", "session.har", `{}`, "har"},
		{"auto", "spec.yaml", `{"log":{"entries":[]}}`, "har"},
		{"auto", "spec.yaml", "openapi: 3.0.0", "openapi"},
		{"har", "whatever", "", "har"},
		{"openapi", "x.har", "", "openapi"}, // explicit format wins over extension
		// The reported bug: a HAR uploaded via the web UI arrives with no filename,
		// so it must be detected structurally from its log/entries shape, not
		// fall through to OpenAPI ("openapi has no paths").
		{"auto", "", `{"log":{"version":"1.2","entries":[{"request":{"method":"GET","url":"http://h/a"}}]}}`, "har"},
		{"auto", "", `{"openapi":"3.0.0","paths":{"/a":{"get":{}}}}`, "openapi"},
		{"auto", "", "swagger: \"2.0\"\npaths:\n  /a:\n    get: {}", "openapi"}, // YAML via substring fallback
		// Access logs: by extension, by combined-format content, and by JSON-lines
		// content (a multi-line JSONL never parses as one OpenAPI/HAR document).
		{"auto", "access.log", "", "accesslog"},
		{"auto", "", `1.1.1.1 - - [10/Jun/2026:10:00:00 +0000] "GET /a HTTP/1.1" 200 1 "-" "ua"`, "accesslog"},
		{"auto", "", `{"time":"2026-06-10T10:00:00Z","method":"GET","path":"/a","status":200}` + "\n" +
			`{"time":"2026-06-10T10:00:01Z","method":"GET","path":"/b","status":200}`, "accesslog"},
		{"accesslog", "x.yaml", "", "accesslog"}, // explicit format wins
	}
	for _, c := range cases {
		if got := detectFormat(c.format, c.name, []byte(c.data)); got != c.want {
			t.Errorf("detectFormat(%q,%q,...) = %q, want %q", c.format, c.name, got, c.want)
		}
	}
}
