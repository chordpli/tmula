package domain

import "fmt"

// WorkloadKind selects how virtual users are generated over a run.
type WorkloadKind string

const (
	// WorkloadClosed runs a fixed number of concurrent users that loop (the
	// original model): simple, but the concurrency is whatever you set.
	WorkloadClosed WorkloadKind = "closed"
	// WorkloadOpen generates user sessions at an arrival rate over time, so the
	// concurrent user count *emerges* from rate x session duration (Little's
	// Law) — the way real, organic traffic behaves.
	WorkloadOpen WorkloadKind = "open"
)

// Valid reports whether k is a known workload kind.
func (k WorkloadKind) Valid() bool { return k == WorkloadClosed || k == WorkloadOpen }

// RateShape shapes the arrival rate over time for an open workload.
type RateShape string

const (
	RateConstant RateShape = "constant"
	RateRamp     RateShape = "ramp"
	RateSpike    RateShape = "spike"
	RateSoak     RateShape = "soak"
)

// Valid reports whether s is a known rate shape.
func (s RateShape) Valid() bool {
	switch s {
	case RateConstant, RateRamp, RateSpike, RateSoak:
		return true
	default:
		return false
	}
}

// ArrivalProfile describes user arrivals per second over time for an open
// workload. It is data; the workload scheduler turns it into a rate(t) function
// (the same split as LoadProfile -> load.NewStrategy).
type ArrivalProfile struct {
	Shape       RateShape `json:"shape"`
	StartRate   float64   `json:"startRate"` // arrivals/sec at t=0 (ramp/spike base)
	PeakRate    float64   `json:"peakRate"`  // arrivals/sec at the peak
	RampSeconds int       `json:"rampSeconds"`
	HoldSeconds int       `json:"holdSeconds"`
}

// ThinkTime is the pause a virtual user takes between steps (uniform in
// [MinMs, MaxMs]); zero means no pause. Real users read and decide between
// actions, so think time makes the load shape realistic rather than robotic.
type ThinkTime struct {
	MinMs int `json:"minMs"`
	MaxMs int `json:"maxMs"`
}

// Validate checks the think-time range is well-formed.
func (t ThinkTime) Validate() error {
	if t.MinMs < 0 || t.MaxMs < 0 || t.MaxMs < t.MinMs {
		return fmt.Errorf("think time: require 0 <= minMs <= maxMs (got %d..%d)", t.MinMs, t.MaxMs)
	}
	return nil
}

// WorkloadModel parameterizes how a run's virtual users are generated.
type WorkloadModel struct {
	Kind WorkloadKind `json:"kind"`

	// Closed model.
	Concurrency int `json:"concurrency,omitempty"`

	// Open model.
	Arrival         ArrivalProfile `json:"arrival,omitempty"`
	DurationSeconds int            `json:"durationSeconds,omitempty"` // how long to keep arriving
	MaxConcurrency  int            `json:"maxConcurrency,omitempty"`  // back-pressure cap (0 = uncapped)

	ThinkTime ThinkTime `json:"thinkTime"`
}

// Validate checks the workload model is runnable.
func (w WorkloadModel) Validate() error {
	if !w.Kind.Valid() {
		return fmt.Errorf("workload: invalid kind %q", w.Kind)
	}
	if err := w.ThinkTime.Validate(); err != nil {
		return err
	}
	switch w.Kind {
	case WorkloadClosed:
		if w.Concurrency <= 0 {
			return fmt.Errorf("workload: closed model needs concurrency > 0")
		}
	case WorkloadOpen:
		if !w.Arrival.Shape.Valid() {
			return fmt.Errorf("workload: invalid arrival shape %q", w.Arrival.Shape)
		}
		if w.Arrival.StartRate < 0 || w.Arrival.PeakRate < 0 {
			return fmt.Errorf("workload: arrival rates must be non-negative")
		}
		if w.Arrival.StartRate <= 0 && w.Arrival.PeakRate <= 0 {
			return fmt.Errorf("workload: open model needs a positive arrival rate")
		}
		if w.DurationSeconds <= 0 {
			return fmt.Errorf("workload: open model needs durationSeconds > 0")
		}
		if w.MaxConcurrency < 0 {
			return fmt.Errorf("workload: maxConcurrency must be >= 0")
		}
	}
	return nil
}
