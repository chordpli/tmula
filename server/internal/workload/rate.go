package workload

import (
	"fmt"
	"math"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// RateFunc maps elapsed run time to the instantaneous arrival rate in
// arrivals/sec. It is the open-model analogue of load.LoadStrategy: the same
// shape logic (constant/ramp/spike/soak), but expressed as a continuous rate the
// Poisson scheduler samples, rather than a target concurrency.
type RateFunc func(elapsed time.Duration) float64

// NewRateFunc builds a RateFunc from an arrival profile. The peak rate is the
// headline arrivals/sec; the start rate is the ramp/spike baseline. When PeakRate
// is zero the StartRate is used as the level (mirroring how load.NewStrategy
// falls back from PeakConcurrency to StartConcurrency), so a constant profile can
// be specified with either field.
func NewRateFunc(p domain.ArrivalProfile) (RateFunc, error) {
	if !p.Shape.Valid() {
		return nil, fmt.Errorf("workload: invalid arrival shape %q", p.Shape)
	}
	if p.StartRate < 0 || p.PeakRate < 0 {
		return nil, fmt.Errorf("workload: arrival rates must be non-negative")
	}
	ramp := time.Duration(p.RampSeconds) * time.Second
	hold := time.Duration(p.HoldSeconds) * time.Second
	level := p.PeakRate
	if level == 0 {
		level = p.StartRate
	}

	switch p.Shape {
	case domain.RateConstant:
		return func(time.Duration) float64 { return level }, nil

	case domain.RateRamp:
		// Linearly interpolate StartRate→PeakRate over the ramp window, then hold
		// PeakRate. Ramp-down (PeakRate < StartRate) falls out of the same formula.
		return func(elapsed time.Duration) float64 {
			if ramp <= 0 || elapsed >= ramp {
				return p.PeakRate
			}
			if elapsed <= 0 {
				return p.StartRate
			}
			frac := float64(elapsed) / float64(ramp)
			return p.StartRate + (p.PeakRate-p.StartRate)*frac
		}, nil

	case domain.RateSpike:
		// Hold StartRate, jump to PeakRate during the spike window
		// [ramp, ramp+hold), then drop back — the rate analogue of SpikeStrategy.
		return func(elapsed time.Duration) float64 {
			if elapsed >= ramp && elapsed < ramp+hold {
				return p.PeakRate
			}
			return p.StartRate
		}, nil

	case domain.RateSoak:
		// Sustain the level for the hold window, then go quiet. A zero hold means
		// sustain indefinitely (the scheduler bounds the run by DurationSeconds).
		return func(elapsed time.Duration) float64 {
			if hold > 0 && elapsed >= hold {
				return 0
			}
			return level
		}, nil

	default:
		return nil, fmt.Errorf("workload: unknown arrival shape %q", p.Shape)
	}
}

// peakRate returns the maximum rate the profile can demand, used to bound the
// Poisson sampler's step so it tracks rate(t) faithfully even when the rate
// climbs (a coarse step could otherwise overshoot a ramp). It never returns a
// value below a small floor so the sampler always makes forward progress.
func peakRate(p domain.ArrivalProfile) float64 {
	r := math.Max(p.StartRate, p.PeakRate)
	if r <= 0 {
		return 1
	}
	return r
}
