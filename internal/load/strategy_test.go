package load

import (
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

func sec(n int) time.Duration { return time.Duration(n) * time.Second }

func TestConstantStrategy(t *testing.T) {
	s := ConstantStrategy{Level: 25}
	for _, e := range []int{0, 5, 100} {
		if got := s.TargetConcurrency(sec(e)); got != 25 {
			t.Errorf("constant at %ds = %d, want 25", e, got)
		}
	}
}

func TestRampInterpolation(t *testing.T) {
	s := RampStrategy{Start: 0, Peak: 100, Ramp: sec(10)}
	cases := map[int]int{0: 0, 5: 50, 10: 100, 15: 100}
	for e, want := range cases {
		if got := s.TargetConcurrency(sec(e)); got != want {
			t.Errorf("ramp at %ds = %d, want %d", e, got, want)
		}
	}
}

func TestRampWithinTolerance(t *testing.T) {
	s := RampStrategy{Start: 10, Peak: 110, Ramp: sec(10)}
	// at 3s expect 10 + 100*0.3 = 40, within +/-10%.
	got := s.TargetConcurrency(sec(3))
	if got < 36 || got > 44 {
		t.Errorf("ramp at 3s = %d, want ~40 (+/-10%%)", got)
	}
}

func TestSpikeProfile(t *testing.T) {
	s := SpikeStrategy{Base: 10, Peak: 100, At: sec(5), Duration: sec(2)}
	cases := map[int]int{0: 10, 4: 10, 5: 100, 6: 100, 7: 10, 20: 10}
	for e, want := range cases {
		if got := s.TargetConcurrency(sec(e)); got != want {
			t.Errorf("spike at %ds = %d, want %d", e, got, want)
		}
	}
}

func TestSoakProfile(t *testing.T) {
	s := SoakStrategy{Level: 50, Hold: sec(30)}
	if s.TargetConcurrency(sec(10)) != 50 {
		t.Error("soak during hold should be at level")
	}
	if s.TargetConcurrency(sec(30)) != 0 {
		t.Error("soak after hold should drop to 0")
	}
}

func TestNewStrategyFromProfile(t *testing.T) {
	mk := func(strat domain.LoadStrategy) domain.LoadProfile {
		return domain.LoadProfile{
			TargetAPIID: "api1", Strategy: strat,
			Shape: domain.ProfileShape{StartConcurrency: 5, PeakConcurrency: 50, RampSeconds: 10, HoldSeconds: 20},
		}
	}
	for strat, wantName := range map[domain.LoadStrategy]string{
		domain.LoadWeight: "constant",
		domain.LoadRamp:   "ramp",
		domain.LoadSpike:  "spike",
		domain.LoadSoak:   "soak",
	} {
		s, err := NewStrategy(mk(strat))
		if err != nil {
			t.Fatalf("NewStrategy(%s): %v", strat, err)
		}
		if s.Name() != wantName {
			t.Errorf("strategy %s -> %s, want %s", strat, s.Name(), wantName)
		}
	}

	if _, err := NewStrategy(domain.LoadProfile{TargetAPIID: "x", Strategy: "bogus"}); err == nil {
		t.Error("unknown strategy should error")
	}
}
