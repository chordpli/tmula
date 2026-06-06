package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chordpli/tmula/internal/scenariofile"
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
	}
	for _, c := range cases {
		if got := detectFormat(c.format, c.name, []byte(c.data)); got != c.want {
			t.Errorf("detectFormat(%q,%q,...) = %q, want %q", c.format, c.name, got, c.want)
		}
	}
}
