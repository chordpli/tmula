package workload

import (
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

func TestNewRateFuncConstant(t *testing.T) {
	r, err := NewRateFunc(domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: 150})
	if err != nil {
		t.Fatalf("NewRateFunc: %v", err)
	}
	for _, at := range []time.Duration{0, time.Second, time.Minute} {
		if got := r(at); got != 150 {
			t.Errorf("constant rate at %v = %v, want 150", at, got)
		}
	}
}

func TestNewRateFuncConstantFallsBackToStartRate(t *testing.T) {
	// PeakRate omitted: StartRate is the level (mirrors load.NewStrategy fallback).
	r, err := NewRateFunc(domain.ArrivalProfile{Shape: domain.RateConstant, StartRate: 42})
	if err != nil {
		t.Fatalf("NewRateFunc: %v", err)
	}
	if got := r(time.Second); got != 42 {
		t.Errorf("rate = %v, want 42", got)
	}
}

func TestNewRateFuncRampIsMonotonic(t *testing.T) {
	r, err := NewRateFunc(domain.ArrivalProfile{
		Shape: domain.RateRamp, StartRate: 10, PeakRate: 110, RampSeconds: 10,
	})
	if err != nil {
		t.Fatalf("NewRateFunc: %v", err)
	}
	if got := r(0); got != 10 {
		t.Errorf("ramp at t=0 = %v, want 10 (start)", got)
	}
	if got := r(5 * time.Second); got != 60 {
		t.Errorf("ramp at half = %v, want 60 (midpoint)", got)
	}
	if got := r(10 * time.Second); got != 110 {
		t.Errorf("ramp at end = %v, want 110 (peak)", got)
	}
	if got := r(20 * time.Second); got != 110 {
		t.Errorf("ramp past end = %v, want 110 (held)", got)
	}
	// Strictly increasing through the ramp window.
	prev := r(0)
	for s := 1; s <= 10; s++ {
		cur := r(time.Duration(s) * time.Second)
		if cur <= prev {
			t.Errorf("ramp not increasing at %ds: %v then %v", s, prev, cur)
		}
		prev = cur
	}
}

func TestNewRateFuncSpikeWindow(t *testing.T) {
	r, err := NewRateFunc(domain.ArrivalProfile{
		Shape: domain.RateSpike, StartRate: 5, PeakRate: 100, RampSeconds: 3, HoldSeconds: 2,
	})
	if err != nil {
		t.Fatalf("NewRateFunc: %v", err)
	}
	cases := []struct {
		at   time.Duration
		want float64
	}{
		{0, 5}, {2 * time.Second, 5}, // before the spike: baseline
		{3 * time.Second, 100}, {4 * time.Second, 100}, // inside [3,5): peak
		{5 * time.Second, 5}, {10 * time.Second, 5}, // after: back to baseline
	}
	for _, c := range cases {
		if got := r(c.at); got != c.want {
			t.Errorf("spike at %v = %v, want %v", c.at, got, c.want)
		}
	}
}

func TestNewRateFuncSoakHoldsThenStops(t *testing.T) {
	r, err := NewRateFunc(domain.ArrivalProfile{
		Shape: domain.RateSoak, PeakRate: 30, HoldSeconds: 5,
	})
	if err != nil {
		t.Fatalf("NewRateFunc: %v", err)
	}
	if got := r(time.Second); got != 30 {
		t.Errorf("soak during hold = %v, want 30", got)
	}
	if got := r(5 * time.Second); got != 0 {
		t.Errorf("soak at hold boundary = %v, want 0 (quiet)", got)
	}
	if got := r(10 * time.Second); got != 0 {
		t.Errorf("soak past hold = %v, want 0 (quiet)", got)
	}
}

func TestNewRateFuncRejectsBadProfile(t *testing.T) {
	if _, err := NewRateFunc(domain.ArrivalProfile{Shape: "bogus", PeakRate: 1}); err == nil {
		t.Error("expected error for invalid shape")
	}
	if _, err := NewRateFunc(domain.ArrivalProfile{Shape: domain.RateConstant, PeakRate: -1}); err == nil {
		t.Error("expected error for negative rate")
	}
}

func TestPeakRateFloor(t *testing.T) {
	// A zero-rate profile still yields a positive ceiling so the sampler makes
	// progress (the rate function itself returns 0, thinning all candidates).
	if got := peakRate(domain.ArrivalProfile{Shape: domain.RateConstant}); got != 1 {
		t.Errorf("peakRate floor = %v, want 1", got)
	}
	if got := peakRate(domain.ArrivalProfile{Shape: domain.RateRamp, StartRate: 7, PeakRate: 70}); got != 70 {
		t.Errorf("peakRate = %v, want 70", got)
	}
}
