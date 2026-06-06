package domain

import (
	"math"
	"testing"
)

func TestWorkloadModelValidate(t *testing.T) {
	closed := WorkloadModel{Kind: WorkloadClosed, Concurrency: 100}
	if err := closed.Validate(); err != nil {
		t.Errorf("valid closed model rejected: %v", err)
	}
	if err := (WorkloadModel{Kind: WorkloadClosed, Concurrency: 0}).Validate(); err == nil {
		t.Error("closed model with 0 concurrency should fail")
	}

	open := WorkloadModel{
		Kind:            WorkloadOpen,
		Arrival:         ArrivalProfile{Shape: RateRamp, StartRate: 1, PeakRate: 100, RampSeconds: 30},
		DurationSeconds: 60,
		MaxConcurrency:  5000,
	}
	if err := open.Validate(); err != nil {
		t.Errorf("valid open model rejected: %v", err)
	}

	bad := []WorkloadModel{
		{Kind: "weird"},
		{Kind: WorkloadOpen, Arrival: ArrivalProfile{Shape: "nope", PeakRate: 1}, DurationSeconds: 10},
		{Kind: WorkloadOpen, Arrival: ArrivalProfile{Shape: RateConstant}, DurationSeconds: 10},             // no rate
		{Kind: WorkloadOpen, Arrival: ArrivalProfile{Shape: RateConstant, PeakRate: 5}, DurationSeconds: 0}, // no window
		{Kind: WorkloadOpen, Arrival: ArrivalProfile{Shape: RateConstant, PeakRate: 5}, DurationSeconds: 10, ThinkTime: ThinkTime{MinMs: 5, MaxMs: 1}},
	}
	for i, w := range bad {
		if err := w.Validate(); err == nil {
			t.Errorf("bad workload[%d] passed validation", i)
		}
	}
}

func TestThinkTimeValidate(t *testing.T) {
	if err := (ThinkTime{MinMs: 100, MaxMs: 500}).Validate(); err != nil {
		t.Errorf("valid think time rejected: %v", err)
	}
	if err := (ThinkTime{}).Validate(); err != nil {
		t.Errorf("zero think time should be valid (no pause): %v", err)
	}
	if err := (ThinkTime{MinMs: 500, MaxMs: 100}).Validate(); err == nil {
		t.Error("max < min should fail")
	}
	if err := (ThinkTime{MinMs: -1}).Validate(); err == nil {
		t.Error("negative think time should fail")
	}
}

func TestLittlesLaw(t *testing.T) {
	// 1,000,000 users / hour, 60s session -> ~16,667 concurrent.
	rate := ArrivalRateForTotal(1_000_000, 3600)
	if math.Abs(rate-277.78) > 0.1 {
		t.Errorf("arrival rate = %.2f/s, want ~277.78", rate)
	}
	conc := EstimatedConcurrency(rate, 60)
	if math.Abs(conc-16_666.7) > 1 {
		t.Errorf("concurrency = %.0f, want ~16,667", conc)
	}
	if got := WorkersNeeded(16_667, 2000); got != 9 {
		t.Errorf("workers = %d, want 9", got)
	}
	if WorkersNeeded(100, 0) != 0 {
		t.Error("unknown per-worker cap should yield 0")
	}
}

func TestPlanCapacity(t *testing.T) {
	p := PlanCapacity(1_000_000, 3600, 60, 2000)
	if p.PeakConcurrency < 16_000 || p.PeakConcurrency > 17_000 {
		t.Errorf("peak concurrency = %d, want ~16,667", p.PeakConcurrency)
	}
	if p.WorkersNeeded != 9 {
		t.Errorf("workers = %d, want 9", p.WorkersNeeded)
	}
	// A small target needs a single worker.
	if s := PlanCapacity(6000, 60, 30, 2000); s.WorkersNeeded != 2 {
		t.Errorf("small plan workers = %d, want 2 (3000 concurrent / 2000)", s.WorkersNeeded)
	}
}
