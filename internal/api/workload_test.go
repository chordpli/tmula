package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// TestOpenModelRun drives an experiment with the open (arrival-rate) workload
// model end-to-end and asserts the scheduler actually generated traffic.
func TestOpenModelRun(t *testing.T) {
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

	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create open experiment = %d", resp.StatusCode)
	}
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)
	if report.Stats.Total == 0 {
		t.Fatal("open-model run produced no requests (scheduler generated no sessions)")
	}
	if report.Stats.Errors != 0 {
		t.Errorf("healthy SUT should yield no errors, got %d", report.Stats.Errors)
	}
}

func TestOpenModelRejectsInvalidWorkload(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()
	sut := sutOK()
	defer sut.Close()

	spec := specFor(sut.URL, 1)
	spec.Workload = &domain.WorkloadModel{Kind: domain.WorkloadOpen} // no rate, no duration
	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid workload = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCapacityEndpoint(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()

	resp, err := http.Get(cp.URL + "/capacity?totalUsers=1000000&windowSeconds=3600&avgSessionSeconds=60&perWorkerCap=2000")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("capacity: %v status=%v", err, resp.StatusCode)
	}
	var plan struct {
		ArrivalPerSec   float64 `json:"arrivalPerSec"`
		PeakConcurrency int     `json:"peakConcurrency"`
		WorkersNeeded   int     `json:"workersNeeded"`
	}
	decode(t, resp, &plan)
	if plan.PeakConcurrency < 16000 || plan.PeakConcurrency > 17000 {
		t.Errorf("peak concurrency = %d, want ~16,667", plan.PeakConcurrency)
	}
	if plan.WorkersNeeded != 9 {
		t.Errorf("workers needed = %d, want 9", plan.WorkersNeeded)
	}

	bad, _ := http.Get(cp.URL + "/capacity?totalUsers=0")
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("missing params should be 400, got %d", bad.StatusCode)
	}
	bad.Body.Close()
}
