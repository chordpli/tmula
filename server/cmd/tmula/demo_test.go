package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/scenario"
	"github.com/chordpli/tmula/server/internal/scenariofile"
	"github.com/chordpli/tmula/server/internal/web"
)

// TestDemoSpecFromEmbeddedLog: the demo's learn step must turn the embedded
// access log into a valid, runnable spec — graph-valid (scenario.Validate),
// open-workload, live-traceable, and allowlisted to exactly the demo SUT's
// host so the demo never widens the safety net.
func TestDemoSpecFromEmbeddedLog(t *testing.T) {
	spec, stats, err := buildDemoSpec("http://127.0.0.1:9999", 60*time.Second)
	if err != nil {
		t.Fatalf("buildDemoSpec: %v", err)
	}
	if err := scenario.Validate(spec.Graph); err != nil {
		t.Errorf("learned graph fails scenario.Validate: %v", err)
	}
	if stats.Requests == 0 || stats.Sessions == 0 {
		t.Errorf("learner stats = %+v, want usable requests/sessions", stats)
	}
	if spec.Workload == nil || !spec.IsOpen() {
		t.Fatal("demo spec must use the learned open workload")
	}
	if spec.Workload.DurationSeconds != 60 {
		t.Errorf("DurationSeconds = %d, want 60 (the --duration window)", spec.Workload.DurationSeconds)
	}
	if !spec.Trace {
		t.Error("demo spec must opt into live tracing (the flow-map view)")
	}
	if c := spec.Workload.MaxConcurrency; c <= 0 || c > 200 {
		t.Errorf("MaxConcurrency = %d, want within (0,200] so per-request tracing stays on", c)
	}
	if len(spec.TargetEnv.Allowlist) != 1 || spec.TargetEnv.Allowlist[0] != "127.0.0.1" {
		t.Errorf("allowlist = %v, want exactly the SUT host (no safety bypass)", spec.TargetEnv.Allowlist)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("demo spec fails RunSpec validation: %v", err)
	}
}

// TestAdaptDemoPacing: the learned workload suggestion is compressed to the
// demo window — enough sessions for every planted bug to surface, think time
// scaled so sessions finish well inside --duration.
func TestAdaptDemoPacing(t *testing.T) {
	// A long demo keeps the window and floors the rate for a lively flow map.
	long := &scenariofile.Open{Rate: 1, ForSeconds: 9999, ThinkMs: []int{4000, 8000}}
	adaptDemoPacing(long, 60*time.Second)
	if long.ForSeconds != 60 {
		t.Errorf("ForSeconds = %d, want 60", long.ForSeconds)
	}
	if long.Rate < demoMinRate {
		t.Errorf("Rate = %v, want at least the %v/s demo floor", long.Rate, demoMinRate)
	}
	if long.ThinkMs[1]*demoThinkDivisor > 60_000 {
		t.Errorf("ThinkMs = %v, want max think <= duration/%d", long.ThinkMs, demoThinkDivisor)
	}
	if long.ThinkMs[0] > long.ThinkMs[1] {
		t.Errorf("ThinkMs = %v, want min <= max after scaling", long.ThinkMs)
	}

	// A short (test-grade) demo raises the rate so the session budget still
	// lands inside the window: bugs at a few percent need volume to surface.
	short := &scenariofile.Open{Rate: 1, ForSeconds: 9999, ThinkMs: []int{4000, 8000}}
	adaptDemoPacing(short, 1500*time.Millisecond)
	if short.ForSeconds != 2 {
		t.Errorf("ForSeconds = %d, want ceil(1.5s) = 2", short.ForSeconds)
	}
	if got := short.Rate * float64(short.ForSeconds); got < demoMinSessions {
		t.Errorf("rate*window = %v sessions, want >= %d so findings are near-certain", got, demoMinSessions)
	}
	if short.Rate > demoMaxRate {
		t.Errorf("Rate = %v, want capped at %v/s", short.Rate, demoMaxRate)
	}

	// An already-faster learned rate is honored, never reduced.
	fast := &scenariofile.Open{Rate: 1000, ForSeconds: 9999, ThinkMs: []int{10, 20}}
	adaptDemoPacing(fast, 60*time.Second)
	if fast.Rate < demoMaxRate {
		t.Errorf("Rate = %v, a learned rate above the floor must not be reduced below the cap", fast.Rate)
	}
}

// TestDemoDryRun drives the whole demo pipeline — planted-bug SUT, learn,
// engine, run, summary — end to end on loopback with a short real run (no
// virtual clock), and asserts it finishes with findings and actionable next
// steps. The browser opener is injected so nothing pops up on a CI box.
func TestDemoDryRun(t *testing.T) {
	var openedURL string
	opts := demoOptions{
		addr:     "127.0.0.1:0",
		duration: 1500 * time.Millisecond,
		openBrowser: func(url string) error {
			openedURL = url
			return nil
		},
	}

	var rep cliReport
	out, err := captureStdoutErr(t, func() error {
		var runErr error
		rep, runErr = runDemoWith(context.Background(), opts)
		return runErr
	})
	if err != nil {
		t.Fatalf("runDemoWith: %v\noutput:\n%s", err, out)
	}

	if rep.Run.Status != "completed" {
		t.Errorf("run status = %q, want completed\noutput:\n%s", rep.Run.Status, out)
	}
	// The planted bugs fire at a few percent each; the demo pacing schedules
	// enough sessions that at least one finding is a statistical certainty.
	if len(rep.Findings) == 0 {
		t.Errorf("demo run produced no findings; the planted bugs must surface\noutput:\n%s", out)
	}
	if openedURL == "" || !strings.HasPrefix(openedURL, "http://") {
		t.Errorf("browser opener got %q, want the engine console URL", openedURL)
	}
	// Fixed contract: the console attaches straight to the run's live view, so
	// the browser opens (and the printed console URLs carry) ?run=<run-id>.
	if !strings.Contains(openedURL, "/?run="+rep.Run.ID) {
		t.Errorf("browser opened %q, want it attached to the run via /?run=%s", openedURL, rep.Run.ID)
	}
	for _, want := range []string{"tmula reproduce", "Findings", "tmula init", "/?run=" + rep.Run.ID} {
		if !strings.Contains(out, want) {
			t.Errorf("demo output is missing %q\noutput:\n%s", want, out)
		}
	}
	// The committed placeholder UI cannot show the live view; the demo must say
	// so (a binary built with `make web` embeds the real console and stays quiet).
	if hasNote := strings.Contains(out, "make web"); hasNote == web.HasBuiltUI() {
		t.Errorf("placeholder note printed=%v but built UI embedded=%v; the demo must warn exactly when the live console is missing\noutput:\n%s",
			hasNote, web.HasBuiltUI(), out)
	}
}

// TestPollRunReportHeartbeat: while the demo waits in [4/4], it prints a short
// heartbeat line every demoHeartbeat — elapsed over the window plus the live
// request/finding counts — so a 30s wait visibly progresses without flooding
// the terminal.
func TestPollRunReportHeartbeat(t *testing.T) {
	old := demoHeartbeat
	demoHeartbeat = 20 * time.Millisecond
	defer func() { demoHeartbeat = old }()

	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/runs/r1/report", func(w http.ResponseWriter, _ *http.Request) {
		var rep cliReport
		rep.Run.ID = "r1"
		rep.Run.Status = "running"
		if calls.Add(1) >= 6 {
			rep.Run.Status = "completed"
		}
		rep.Stats.Total = 42
		rep.Findings = []cliFinding{{Category: "http_5xx"}}
		_ = json.NewEncoder(w).Encode(rep)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	out, err := captureStdoutErr(t, func() error {
		_, pollErr := pollRunReport(context.Background(), ts.URL, "r1", 30*time.Second)
		return pollErr
	})
	if err != nil {
		t.Fatalf("pollRunReport: %v", err)
	}
	for _, want := range []string{"/30s", "requests 42", "findings 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("heartbeat output is missing %q\noutput:\n%s", want, out)
		}
	}
}

// TestDemoNoBrowser: --no-browser must skip the opener entirely.
func TestDemoNoBrowser(t *testing.T) {
	called := false
	opts := demoOptions{
		addr:      "127.0.0.1:0",
		duration:  time.Second,
		noBrowser: true,
		openBrowser: func(string) error {
			called = true
			return nil
		},
	}
	if _, err := captureStdoutErr(t, func() error {
		_, runErr := runDemoWith(context.Background(), opts)
		return runErr
	}); err != nil {
		t.Fatalf("runDemoWith: %v", err)
	}
	if called {
		t.Error("--no-browser must not invoke the browser opener")
	}
}

// TestDemoEnginePortConflict: a taken --addr must fail fast with a friendly
// pointer at the flag, not a bare bind error after the SUT already started.
func TestDemoEnginePortConflict(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	opts := demoOptions{addr: ln.Addr().String(), duration: time.Second, noBrowser: true}
	_, err = captureStdoutErr(t, func() error {
		_, runErr := runDemoWith(context.Background(), opts)
		return runErr
	})
	if err == nil {
		t.Fatal("a taken engine port should error")
	}
	if !strings.Contains(err.Error(), "--addr") || !strings.Contains(err.Error(), ln.Addr().String()) {
		t.Errorf("error = %q, want it to name the address and suggest --addr", err)
	}
}

// TestDemoInterrupted: a canceled context (Ctrl-C) ends the demo cleanly — the
// servers shut down via the deferred stops and no error is reported.
func TestDemoInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already interrupted: the poll loop must exit immediately
	opts := demoOptions{addr: "127.0.0.1:0", duration: time.Minute, noBrowser: true}
	out, err := captureStdoutErr(t, func() error {
		_, runErr := runDemoWith(ctx, opts)
		return runErr
	})
	if err != nil {
		t.Fatalf("an interrupt must exit cleanly, got %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "interrupted") {
		t.Errorf("output should acknowledge the interrupt\noutput:\n%s", out)
	}
}

// TestDemoArgErrors: flag validation fails fast, before anything starts.
func TestDemoArgErrors(t *testing.T) {
	if err := runDemo([]string{"--duration", "0s"}); err == nil {
		t.Error("a non-positive --duration should error")
	}
	if err := runDemo([]string{"unexpected-arg"}); err == nil {
		t.Error("positional arguments should error")
	}
}
