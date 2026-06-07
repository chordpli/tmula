package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chordpli/tmula/internal/bench"
)

// TestBenchSingleEndpointInProcess drives `tmula bench --target ... --get /`
// end to end against an httptest SUT and asserts that the result has
// TotalRequests > 0 and a finite AchievedRPS.
func TestBenchSingleEndpointInProcess(t *testing.T) {
	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	out, err := captureStdoutErr(t, func() error {
		return runBench([]string{
			"--target", sut.URL,
			"--get", "/",
			"--users", "5",
			"--max-steps", "1",
			"--timeout", "5s",
			"--json",
		})
	})
	if err != nil {
		t.Fatalf("runBench: %v\noutput:\n%s", err, out)
	}

	var result bench.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse bench result JSON: %v\n%s", err, out)
	}
	if result.TotalRequests <= 0 {
		t.Errorf("TotalRequests = %d, want > 0", result.TotalRequests)
	}
	if math.IsNaN(result.AchievedRPS) || math.IsInf(result.AchievedRPS, 0) {
		t.Errorf("AchievedRPS = %v, want a finite value", result.AchievedRPS)
	}
	if result.TargetConcurrency != 5 {
		t.Errorf("TargetConcurrency = %d, want 5", result.TargetConcurrency)
	}
}

// TestBenchScenarioFileInProcess drives bench from a YAML scenario file.
func TestBenchScenarioFileInProcess(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	file := filepath.Join(t.TempDir(), "scenario.yaml")
	doc := "target: " + sut.URL + "\nflow:\n  - id: a\n    request: GET /a\nusers: 3\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	out, err := captureStdoutErr(t, func() error {
		return runBench([]string{file, "--users", "3", "--max-steps", "1", "--json"})
	})
	if err != nil {
		t.Fatalf("runBench: %v\noutput:\n%s", err, out)
	}

	var result bench.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse bench result JSON: %v\n%s", err, out)
	}
	if result.TotalRequests <= 0 {
		t.Errorf("TotalRequests = %d, want > 0", result.TotalRequests)
	}
}

// TestBenchArgErrors checks that missing scenario/target gives a clear error.
func TestBenchArgErrors(t *testing.T) {
	if err := runBench([]string{}); err == nil {
		t.Error("no scenario file and no flags should error")
	}
	if err := runBench([]string{"--get", "/"}); err == nil {
		t.Error("single-endpoint mode without --target should error")
	}
	if err := runBench([]string{"a.yaml", "b.yaml"}); err == nil {
		t.Error("more than one positional scenario file should error")
	}
}

// TestBenchHumanReadableOutput checks the non-JSON output path contains expected labels.
func TestBenchHumanReadableOutput(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	out, err := captureStdoutErr(t, func() error {
		return runBench([]string{
			"--target", sut.URL,
			"--get", "/",
			"--users", "3",
			"--max-steps", "1",
		})
	})
	if err != nil {
		t.Fatalf("runBench: %v\noutput:\n%s", err, out)
	}
	for _, label := range []string{"Bench", "achieved RPS", "total requests", "error rate", "tracking error", "p50"} {
		if !strings.Contains(out, label) {
			t.Errorf("human output missing %q\n%s", label, out)
		}
	}
}

// TestRunDispatchStillWorks guards that wiring bench into main.go did not break
// the existing run/init dispatch.
func TestRunDispatchStillWorks(t *testing.T) {
	// --version exercises the serve branch (which exits early), confirming the
	// switch falls through correctly for unrecognized subcommands.
	if err := run([]string{"--version"}); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	// bench with no args should error (not panic or route to serve).
	if err := run([]string{"bench"}); err == nil {
		t.Error("tmula bench with no args should error")
	}
}
