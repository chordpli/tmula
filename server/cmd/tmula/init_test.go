package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestImportRunSpecWithStatsLearnsFromAccessLogWithoutTarget(t *testing.T) {
	// The web import endpoint only consumes graph/templates/start/maxSteps, so
	// a log (which names no host) must still convert instead of failing on the
	// missing target — and the access-log path must carry the learner's
	// coverage stats, mapped field-for-field onto the wire type.
	garbled := sampleAccessLog + "definitely not an access log line\n"
	spec, stats, err := importRunSpecWithStats([]byte(garbled), "auto")
	if err != nil {
		t.Fatalf("importRunSpecWithStats: %v", err)
	}
	if len(spec.Graph.Nodes) < 3 || spec.Start == "" {
		t.Errorf("learned spec graph=%d nodes start=%q, want a populated graph", len(spec.Graph.Nodes), spec.Start)
	}
	if stats == nil {
		t.Fatal("stats = nil, want the learner's coverage report for an access-log import")
	}
	if stats.Requests != 3 || stats.Sessions != 2 || stats.Clients != 2 {
		t.Errorf("stats = %+v, want requests/sessions/clients = 3/2/2", stats)
	}
	if stats.Format != "combined" {
		t.Errorf("stats.Format = %q, want the detected profile (combined)", stats.Format)
	}
	if stats.Skipped != 1 || len(stats.SkippedSamples) != 1 {
		t.Fatalf("skipped = %d with %d sample(s), want the garbage line counted and sampled", stats.Skipped, len(stats.SkippedSamples))
	}
	if s := stats.SkippedSamples[0]; s.Line != 4 || s.Reason == "" || s.Text == "" {
		t.Errorf("skipped sample = %+v, want line 4 with a reason and the line text", s)
	}
}

func TestImportRunSpecWithStatsNilForSpecConversions(t *testing.T) {
	// OpenAPI/HAR conversions have no coverage to report: stats must be nil so
	// the endpoint omits the field and old clients see the pre-stats shape.
	openapi := "openapi: 3.0.0\nservers:\n  - url: http://svc.test\npaths:\n  /ping:\n    get: {}\n"
	_, stats, err := importRunSpecWithStats([]byte(openapi), "auto")
	if err != nil {
		t.Fatalf("importRunSpecWithStats: %v", err)
	}
	if stats != nil {
		t.Errorf("stats = %+v, want nil for an OpenAPI conversion", stats)
	}
}

func TestImportScenarioNoteReportsFormatAndSkippedLines(t *testing.T) {
	// `tmula init` must not stay silent about what the learner inferred: the
	// note carries the detected format (so a misdetection is visible) and the
	// first skipped lines with their line numbers and reasons.
	garbled := sampleAccessLog + "definitely not an access log line\n"
	_, note, err := importScenario([]byte(garbled), "accesslog", "")
	if err != nil {
		t.Fatalf("importScenario: %v", err)
	}
	for _, want := range []string{"combined", "skipped 1 unusable line(s)", "line 4:"} {
		if !strings.Contains(note, want) {
			t.Errorf("note is missing %q\nnote:\n%s", want, note)
		}
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
