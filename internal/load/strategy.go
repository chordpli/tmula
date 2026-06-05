package load

import (
	"fmt"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// LoadStrategy maps elapsed run time to a target concurrency level. It is the
// extension point for how load is shaped over time; a future absolute-RPS
// strategy implements the same interface without touching callers.
type LoadStrategy interface {
	// TargetConcurrency returns the desired number of active virtual users at
	// the given elapsed time since the run started.
	TargetConcurrency(elapsed time.Duration) int
	Name() string
}

// ConstantStrategy holds a steady level (the "weight"/soak baseline).
type ConstantStrategy struct{ Level int }

func (s ConstantStrategy) TargetConcurrency(time.Duration) int { return s.Level }
func (s ConstantStrategy) Name() string                        { return "constant" }

// RampStrategy ramps linearly from Start to Peak over Ramp, then holds Peak.
type RampStrategy struct {
	Start, Peak int
	Ramp        time.Duration
}

func (s RampStrategy) TargetConcurrency(elapsed time.Duration) int {
	if s.Ramp <= 0 || elapsed >= s.Ramp {
		return s.Peak
	}
	if elapsed <= 0 {
		return s.Start
	}
	frac := float64(elapsed) / float64(s.Ramp)
	return s.Start + int(float64(s.Peak-s.Start)*frac+0.5)
}
func (s RampStrategy) Name() string { return "ramp" }

// SpikeStrategy holds Base, jumps to Peak during [At, At+Duration), then drops.
type SpikeStrategy struct {
	Base, Peak int
	At         time.Duration
	Duration   time.Duration
}

func (s SpikeStrategy) TargetConcurrency(elapsed time.Duration) int {
	if elapsed >= s.At && elapsed < s.At+s.Duration {
		return s.Peak
	}
	return s.Base
}
func (s SpikeStrategy) Name() string { return "spike" }

// SoakStrategy holds Level for Hold, then drops to zero (sustained load test).
type SoakStrategy struct {
	Level int
	Hold  time.Duration
}

func (s SoakStrategy) TargetConcurrency(elapsed time.Duration) int {
	if s.Hold > 0 && elapsed >= s.Hold {
		return 0
	}
	return s.Level
}
func (s SoakStrategy) Name() string { return "soak" }

// NewStrategy builds a LoadStrategy from a load profile. The profile's target
// API (p.TargetAPIID) is where the concentrated load is aimed; this strategy
// shapes how that concentration varies over time.
func NewStrategy(p domain.LoadProfile) (LoadStrategy, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	sh := p.Shape
	ramp := time.Duration(sh.RampSeconds) * time.Second
	hold := time.Duration(sh.HoldSeconds) * time.Second
	level := sh.PeakConcurrency
	if level == 0 {
		level = sh.StartConcurrency
	}
	switch p.Strategy {
	case domain.LoadWeight:
		return ConstantStrategy{Level: level}, nil
	case domain.LoadRamp:
		return RampStrategy{Start: sh.StartConcurrency, Peak: sh.PeakConcurrency, Ramp: ramp}, nil
	case domain.LoadSpike:
		return SpikeStrategy{Base: sh.StartConcurrency, Peak: sh.PeakConcurrency, At: ramp, Duration: hold}, nil
	case domain.LoadSoak:
		return SoakStrategy{Level: level, Hold: hold}, nil
	default:
		return nil, fmt.Errorf("load: unknown strategy %q", p.Strategy)
	}
}
