package load

import "testing"

// TestRampDownRounding guards the sign-aware rounding fix: a ramp from a high
// Start down to a lower Peak must interpolate correctly (int(x+0.5) would round
// the wrong way for the negative delta).
func TestRampDownRounding(t *testing.T) {
	s := RampStrategy{Start: 100, Peak: 0, Ramp: sec(10)}
	if got := s.TargetConcurrency(sec(3)); got != 70 { // 100 + (0-100)*0.3 = 70
		t.Errorf("ramp-down at 3s = %d, want 70", got)
	}
	if got := s.TargetConcurrency(sec(7)); got != 30 { // 100 + (0-100)*0.7 = 30
		t.Errorf("ramp-down at 7s = %d, want 30", got)
	}
}
