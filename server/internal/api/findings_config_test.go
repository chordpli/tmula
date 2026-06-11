package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
)

// runToReport creates the experiment, starts a run and waits for it to
// complete, returning the final report. It is the shared body of the findings
// configuration tests below.
func runToReport(t *testing.T, cpURL string, spec RunSpec) Report {
	t.Helper()
	resp := postJSON(t, cpURL+"/experiments", spec)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create experiment = %d", resp.StatusCode)
	}
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cpURL+"/experiments/"+created.ID+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d", resp.StatusCode)
	}
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)
	return waitForStatus(t, cpURL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)
}

// hasThresholdFinding reports whether the report carries a threshold finding
// with the given metric-identity evidence ref ("error-rate" / "p95-latency").
func hasThresholdFinding(rep Report, ref string) bool {
	for _, f := range rep.Findings {
		if f.Category == domain.FindingThreshold && f.EvidenceRef == ref {
			return true
		}
	}
	return false
}

// TestClosedRunUsesConfiguredP95 drives the closed in-process path end to end
// with a findings block: a p95 gate far below any real round trip must produce
// the p95-latency threshold finding that was unreachable while the control
// plane hardcoded its ClassifyConfig (p95 disabled).
func TestClosedRunUsesConfiguredP95(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 5)
	spec.Findings = &obs.FindingConfig{P95LatencyMs: 0.0001}
	rep := runToReport(t, cp.URL, spec)
	if !hasThresholdFinding(rep, "p95-latency") {
		t.Fatalf("configured p95 gate produced no p95-latency finding, got %v", rep.Findings)
	}
}

// TestOpenRunUsesConfiguredP95 asserts the open (arrival-rate) path threads
// the same findings block into its scheduler's classifier.
func TestOpenRunUsesConfiguredP95(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 1) // a single identity; open model generates sessions
	spec.Workload = &domain.WorkloadModel{
		Kind:            domain.WorkloadOpen,
		Arrival:         domain.ArrivalProfile{Shape: domain.RateConstant, StartRate: 40, PeakRate: 40},
		DurationSeconds: 1,
		MaxConcurrency:  200,
	}
	spec.Findings = &obs.FindingConfig{P95LatencyMs: 0.0001}
	rep := runToReport(t, cp.URL, spec)
	if !hasThresholdFinding(rep, "p95-latency") {
		t.Fatalf("open run with configured p95 gate produced no p95-latency finding, got %v", rep.Findings)
	}
}

// TestRunWithoutFindingsKeepsDefaultThresholds pins backward compatibility at
// the orchestrator level: with no findings block, a 50% error rate still trips
// the long-standing 0.2 default, and the disabled-by-default p95 gate stays
// silent.
func TestRunWithoutFindingsKeepsDefaultThresholds(t *testing.T) {
	// /b fails so half the requests of the two-node walk error out.
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/b" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	rep := runToReport(t, cp.URL, specFor(sut.URL, 5))
	if !hasThresholdFinding(rep, "error-rate") {
		t.Fatalf("error rate 0.5 should trip the default 0.2 threshold, got %v", rep.Findings)
	}
	if hasThresholdFinding(rep, "p95-latency") {
		t.Fatalf("p95 gate must stay disabled without a findings block, got %v", rep.Findings)
	}
}

// TestCreateRejectsInvalidFindings: a malformed findings block (rate outside
// [0,1]) is rejected at experiment-creation time, like every other spec error.
func TestCreateRejectsInvalidFindings(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 1)
	spec.Findings = &obs.FindingConfig{ErrorRate: 1.5}
	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid findings block = %d, want 400", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	decode(t, resp, &body)
	if !strings.Contains(body.Error, "errorRate") {
		t.Errorf("error %q should mention errorRate", body.Error)
	}
}
