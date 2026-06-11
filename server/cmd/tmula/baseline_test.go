package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/gate"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what it
// printed along with fn's error. The expired-suppression warning goes to stderr
// (stdout may be a JSON document), so tests need to read it separately.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	runErr := fn()
	_ = w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// failingSUT returns a server that always 500s, which reliably produces
// findings (the same finding keys on every run — that determinism is what the
// baseline gate is built on).
func failingSUT() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
}

// captureBaseline runs the SUT once with --json and writes the report to a file,
// returning the path and the parsed report — the exact artifact a CI job would
// save from its main-branch run.
func captureBaseline(t *testing.T, sutURL string) (string, cliReport) {
	t.Helper()
	out := captureStdout(t, func() error {
		return runScenario([]string{"--target", sutURL, "--get", "/", "--users", "5", "--json"})
	})
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse baseline report: %v\n%s", err, out)
	}
	if len(rep.Findings) == 0 {
		t.Fatal("a 500-only SUT should produce findings to gate on")
	}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	return path, rep
}

// TestBaselinePersistingFindingsPass: the same SUT misbehaving the same way
// produces the same finding keys, so a baseline from a previous run makes the
// gate pass — same issues, nothing new — even though --fail-on-findings would
// have failed it.
func TestBaselinePersistingFindingsPass(t *testing.T) {
	sut := failingSUT()
	defer sut.Close()
	baseline, _ := captureBaseline(t, sut.URL)

	out := captureStdout(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5", "--baseline-file", baseline})
	})
	if !strings.Contains(out, "persisting") {
		t.Errorf("gate output should classify findings as persisting:\n%s", out)
	}
}

// TestBaselineNewFindingFails: against a clean baseline (no findings), the same
// failing run is all-new and must exit non-zero via errNewFindings.
func TestBaselineNewFindingFails(t *testing.T) {
	sut := failingSUT()
	defer sut.Close()

	clean := filepath.Join(t.TempDir(), "clean.json")
	if err := os.WriteFile(clean, []byte(`{"findings":[]}`), 0o644); err != nil {
		t.Fatalf("write clean baseline: %v", err)
	}

	_, err := captureStdoutErr(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5", "--baseline-file", clean})
	})
	if !errors.Is(err, errNewFindings) {
		t.Fatalf("err = %v, want errNewFindings", err)
	}
}

// knownIssuesFor renders a known-issues YAML file covering every finding in the
// report with the given expiry date.
func knownIssuesFor(t *testing.T, rep cliReport, expires string) string {
	t.Helper()
	var sb strings.Builder
	for _, f := range rep.Findings {
		fmt.Fprintf(&sb, "- category: %q\n  evidenceRef: %q\n  reason: tracked in TICKET-9\n  expires: %q\n",
			f.Category, f.EvidenceRef, expires)
	}
	path := filepath.Join(t.TempDir(), "known.yaml")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write known issues: %v", err)
	}
	return path
}

// TestBaselineSuppressedFindingsPass: new findings all covered by active known
// issues do not fail the gate, and the output marks them suppressed. The same
// run also writes the four-bucket table into the markdown summary.
func TestBaselineSuppressedFindingsPass(t *testing.T) {
	sut := failingSUT()
	defer sut.Close()
	_, rep := captureBaseline(t, sut.URL)

	clean := filepath.Join(t.TempDir(), "clean.json")
	if err := os.WriteFile(clean, []byte(`{"findings":[]}`), 0o644); err != nil {
		t.Fatalf("write clean baseline: %v", err)
	}
	known := knownIssuesFor(t, rep, time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02"))
	summary := filepath.Join(t.TempDir(), "summary.md")

	out := captureStdout(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5",
			"--baseline-file", clean, "--known-issues", known, "--summary", summary})
	})
	if !strings.Contains(out, "suppressed") {
		t.Errorf("gate output should mark suppressed findings:\n%s", out)
	}

	md, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	for _, want := range []string{"Baseline gate", "suppressed", "TICKET-9"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("markdown summary missing %q:\n%s", want, md)
		}
	}
}

// TestBaselineExpiredSuppressionFails: once the known issue expires, the gate
// reddens again and a warning names the expired entries.
func TestBaselineExpiredSuppressionFails(t *testing.T) {
	sut := failingSUT()
	defer sut.Close()
	_, rep := captureBaseline(t, sut.URL)

	clean := filepath.Join(t.TempDir(), "clean.json")
	if err := os.WriteFile(clean, []byte(`{"findings":[]}`), 0o644); err != nil {
		t.Fatalf("write clean baseline: %v", err)
	}
	known := knownIssuesFor(t, rep, "2000-01-01")

	stderr, err := captureStderr(t, func() error {
		_, err := captureStdoutErr(t, func() error {
			return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5",
				"--baseline-file", clean, "--known-issues", known})
		})
		return err
	})
	if !errors.Is(err, errNewFindings) {
		t.Fatalf("err = %v, want errNewFindings (expired suppression must not pass the gate)", err)
	}
	if !strings.Contains(stderr, "expired") {
		t.Errorf("stderr should warn about expired known issues:\n%s", stderr)
	}
}

// TestBaselineGatePrecedence: --fail-on-findings is the absolute gate and wins
// over a passing baseline gate — both flags together must still fail with
// errFindings when any finding exists.
func TestBaselineGatePrecedence(t *testing.T) {
	sut := failingSUT()
	defer sut.Close()
	baseline, _ := captureBaseline(t, sut.URL)

	_, err := captureStdoutErr(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5",
			"--baseline-file", baseline, "--fail-on-findings"})
	})
	if !errors.Is(err, errFindings) {
		t.Fatalf("err = %v, want errFindings (absolute gate outranks the baseline gate)", err)
	}
}

// TestBaselineRunIDFetchesFromEngine: --baseline <run-id> resolves the baseline
// report through the engine's HTTP API — the CLI's one existing path to
// persisted runs (the engine fronts the Store).
func TestBaselineRunIDFetchesFromEngine(t *testing.T) {
	finding := cliFinding{Category: "threshold", Severity: "warning",
		Description: "error rate 1.00 exceeded threshold 0.20", EvidenceRef: "error-rate"}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/experiments", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "exp-1"})
	})
	mux.HandleFunc("/api/experiments/exp-1/run", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"runId": "run-1"})
	})
	mux.HandleFunc("/api/runs/run-1/report", func(w http.ResponseWriter, _ *http.Request) {
		var rep cliReport
		rep.Run.ID, rep.Run.Status = "run-1", "completed"
		rep.Findings = []cliFinding{finding}
		_ = json.NewEncoder(w).Encode(rep)
	})
	mux.HandleFunc("/api/runs/base-1/report", func(w http.ResponseWriter, _ *http.Request) {
		var rep cliReport
		rep.Run.ID, rep.Run.Status = "base-1", "completed"
		rep.Findings = []cliFinding{finding}
		_ = json.NewEncoder(w).Encode(rep)
	})
	engine := httptest.NewServer(mux)
	defer engine.Close()

	out := captureStdout(t, func() error {
		return runScenario([]string{"--target", "http://sut.invalid", "--get", "/",
			"--engine", engine.URL, "--baseline", "base-1"})
	})
	if !strings.Contains(out, "persisting") {
		t.Errorf("identical baseline via run id should classify as persisting:\n%s", out)
	}

	// An unknown baseline run id is a hard error before any gating.
	_, err := captureStdoutErr(t, func() error {
		return runScenario([]string{"--target", "http://sut.invalid", "--get", "/",
			"--engine", engine.URL, "--baseline", "nope"})
	})
	if err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Errorf("missing baseline run should fail loud, got %v", err)
	}
}

// TestBaselineFlagValidation pins the flag contract: the two baseline sources
// are mutually exclusive, a run-id baseline needs an engine that can have
// history, and known issues without a baseline gate would silently do nothing.
func TestBaselineFlagValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"both baseline forms", []string{"--baseline", "r1", "--baseline-file", "f.json"}, "one of"},
		{"run id without engine", []string{"--baseline", "r1"}, "--engine"},
		{"known issues without baseline", []string{"--known-issues", "k.yaml"}, "--baseline"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := runScenario(append([]string{"--target", "http://sut.invalid", "--get", "/"}, c.args...))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want mention of %q", err, c.want)
			}
		})
	}
}

// TestBaselineBadFilesFailBeforeRunning: an unreadable baseline file or a
// malformed known-issues file must fail before the (potentially long) run
// starts, not after.
func TestBaselineBadFilesFailBeforeRunning(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	err := runScenario([]string{"--target", "http://sut.invalid", "--get", "/", "--baseline-file", missing})
	if err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Errorf("missing baseline file: err = %v", err)
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if werr := os.WriteFile(bad, []byte("- category: threshold\n  evidenceRef: x\n  reason: r\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	base := filepath.Join(t.TempDir(), "base.json")
	if werr := os.WriteFile(base, []byte(`{"findings":[]}`), 0o644); werr != nil {
		t.Fatal(werr)
	}
	err = runScenario([]string{"--target", "http://sut.invalid", "--get", "/",
		"--baseline-file", base, "--known-issues", bad})
	if err == nil || !strings.Contains(err.Error(), "expires") {
		t.Errorf("invalid known-issues file: err = %v", err)
	}
}

// TestMarkdownBaselineGateTable: the summary section tabulates all four buckets
// with the suppression note, and escapes markdown-active characters.
func TestMarkdownBaselineGateTable(t *testing.T) {
	res := gate.Result{
		New: []domain.Finding{{Category: domain.FindingAvailability, Severity: domain.SeverityCritical,
			EvidenceRef: "checkout", Description: "6 consecutive failures | on checkout"}},
		Resolved: []domain.Finding{{Category: domain.FindingContract, Severity: domain.SeverityCritical,
			EvidenceRef: "orders", Description: "2 contract violation(s) on orders"}},
		Persisting: []domain.Finding{{Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
			EvidenceRef: "error-rate", Description: "error rate 0.44 exceeded threshold 0.20"}},
		Suppressed: []gate.Suppressed{{
			Finding: domain.Finding{Category: domain.FindingContract, Severity: domain.SeverityCritical,
				EvidenceRef: "cart", Description: "1 contract violation(s) on cart"},
			Issue: gate.KnownIssue{Category: "contract", EvidenceRef: "cart",
				Reason: "fix in review (TICKET-9)", Expires: "2026-07-01"},
		}},
		Expired: []gate.KnownIssue{{Category: "mutation", EvidenceRef: "old", Reason: "long fixed", Expires: "2026-01-01"}},
	}

	md := markdownBaselineGate(res, "base-1")
	for _, want := range []string{
		"Baseline gate",
		"base-1",
		"1 new", "1 resolved", "1 persisting", "1 suppressed",
		"checkout", "orders", "error-rate", "cart",
		"TICKET-9", "2026-07-01", // the suppression note carries reason and expiry
		"expired",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("baseline gate section missing %q:\n%s", want, md)
		}
	}
	// A pipe inside a description must not break the table row.
	if !strings.Contains(md, `failures \| on`) {
		t.Errorf("description not escaped for a table cell:\n%s", md)
	}
}

// TestToDomainFindings: the CLI's JSON-mirror findings convert to domain
// findings preserving the gate identity (category, evidenceRef) and display
// fields.
func TestToDomainFindings(t *testing.T) {
	got := toDomainFindings([]cliFinding{{
		Category: "threshold", Severity: "warning",
		Description: "error rate 0.31 exceeded threshold 0.20", EvidenceRef: "error-rate",
	}})
	want := domain.Finding{Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
		Description: "error rate 0.31 exceeded threshold 0.20", EvidenceRef: "error-rate"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("toDomainFindings = %+v, want %+v", got, want)
	}
}
