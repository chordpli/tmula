package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// printed. It fails the test if fn returns an error.
func captureStdout(t *testing.T, fn func() error) string {
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
	if runErr != nil {
		t.Fatalf("runScenario: %v\noutput:\n%s", runErr, out)
	}
	return string(out)
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

func TestRunScenarioArgErrors(t *testing.T) {
	if err := runScenario([]string{}); err == nil {
		t.Error("no scenario file and no flags should error")
	}
	if err := runScenario([]string{"--get", "/"}); err == nil {
		t.Error("single-endpoint mode without --target should error")
	}
}
