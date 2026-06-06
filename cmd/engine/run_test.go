package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// captureStdoutErr runs fn with os.Stdout redirected to a pipe and returns what
// it printed along with fn's error.
func captureStdoutErr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// captureStdout is captureStdoutErr that fails the test if fn errors.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	out, err := captureStdoutErr(t, fn)
	if err != nil {
		t.Fatalf("runScenario: %v\noutput:\n%s", err, out)
	}
	return out
}

// TestRunSingleEndpointInProcess drives `tmula run --target ... --get /` end to
// end against an httptest SUT, using the in-process engine (no separate server).
func TestRunSingleEndpointInProcess(t *testing.T) {
	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	out := captureStdout(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "3", "--json"})
	})

	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" {
		t.Errorf("status = %q, want completed", rep.Run.Status)
	}
	if rep.Stats.Total != 3 {
		t.Errorf("total = %d, want 3 (one request per user)", rep.Stats.Total)
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Errorf("SUT hits = %d, want 3", got)
	}
}

// TestRunScenarioFileInProcess drives a run from a YAML scenario file.
func TestRunScenarioFileInProcess(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	file := filepath.Join(t.TempDir(), "scenario.yaml")
	doc := "target: " + sut.URL + "\nflow:\n  - id: a\n    request: GET /a\nusers: 4\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	out := captureStdout(t, func() error {
		return runScenario([]string{file, "--json"})
	})
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" || rep.Stats.Total != 4 {
		t.Errorf("got status=%q total=%d, want completed/4", rep.Run.Status, rep.Stats.Total)
	}
}

// TestRunFailOnFindings checks the CI gate: a SUT that always 5xxs produces
// findings, so --fail-on-findings makes the run return errFindings; without the
// flag the same run succeeds (findings are output, not a failure).
func TestRunFailOnFindings(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sut.Close()

	_, err := captureStdoutErr(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5", "--fail-on-findings"})
	})
	if !errors.Is(err, errFindings) {
		t.Fatalf("err = %v, want errFindings", err)
	}

	// Same run without the flag must not error.
	captureStdout(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5"})
	})
}

func TestGatingFindings(t *testing.T) {
	fs := []cliFinding{{Severity: "warning"}, {Severity: "critical"}, {Severity: "critical"}}
	cases := []struct {
		failAny bool
		minSev  string
		want    int
	}{
		{false, "", 0},         // no gate requested
		{true, "", 3},          // any finding
		{false, "warning", 3},  // warning is the lowest level -> all
		{false, "critical", 2}, // criticals only
		{true, "critical", 2},  // severity refines the bool
	}
	for _, c := range cases {
		if got := gatingFindings(fs, c.failAny, c.minSev); got != c.want {
			t.Errorf("gatingFindings(failAny=%v, minSev=%q) = %d, want %d", c.failAny, c.minSev, got, c.want)
		}
	}
}

// TestFailOnSeverity uses a 400-only SUT (which yields a threshold WARNING
// finding but no criticals) to check the severity gate end to end.
func TestFailOnSeverity(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer sut.Close()
	args := func(extra ...string) []string {
		return append([]string{"--target", sut.URL, "--get", "/", "--users", "5"}, extra...)
	}

	// critical-only gate: there are no criticals, so the run is not failed.
	captureStdout(t, func() error { return runScenario(args("--fail-on-severity", "critical")) })

	// warning gate: the threshold warning trips it.
	if _, err := captureStdoutErr(t, func() error { return runScenario(args("--fail-on-severity", "warning")) }); !errors.Is(err, errFindings) {
		t.Fatalf("warning gate err = %v, want errFindings", err)
	}

	// an invalid severity is rejected before the run starts.
	if err := runScenario(args("--fail-on-severity", "bogus")); err == nil {
		t.Error("invalid --fail-on-severity should error")
	}
}

func TestRunScenarioArgErrors(t *testing.T) {
	if err := runScenario([]string{}); err == nil {
		t.Error("no scenario file and no flags should error")
	}
	if err := runScenario([]string{"--get", "/"}); err == nil {
		t.Error("single-endpoint mode without --target should error")
	}
}
