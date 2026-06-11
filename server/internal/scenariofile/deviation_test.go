package scenariofile

import (
	"strings"
	"testing"
)

// TestExpandCarriesDeviationRate wires the compact file's deviationRate into
// the experiment params, where the run path reads it — replacing the old
// hardcoded 0 that silently disabled deviation for every file-authored run.
func TestExpandCarriesDeviationRate(t *testing.T) {
	s, err := Parse([]byte(`
target: http://localhost:9000
deviationRate: 0.25
flow:
  - id: browse
    request: GET /browse
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got := spec.Experiment.Params.DeviationRate; got != 0.25 {
		t.Errorf("deviationRate = %v, want 0.25", got)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded spec failed validation: %v", err)
	}
}

// TestExpandDefaultsDeviationRateToZero pins the default: a file that does not
// mention deviation expands to rate 0, the happy-path-only behavior every
// existing scenario file already relies on.
func TestExpandDefaultsDeviationRateToZero(t *testing.T) {
	spec, err := Expand(Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got := spec.Experiment.Params.DeviationRate; got != 0 {
		t.Errorf("deviationRate = %v, want default 0", got)
	}
}

// TestExpandRejectsDeviationRateOutOfRange fails fast on a malformed rate with
// a scenariofile-prefixed message, instead of deferring to spec validation.
func TestExpandRejectsDeviationRateOutOfRange(t *testing.T) {
	for _, rate := range []float64{-0.1, 1.5} {
		_, err := Expand(Scenario{
			Target:        "http://h:1",
			DeviationRate: rate,
			Flow:          []Step{{ID: "a", Request: "GET /a"}},
		})
		if err == nil {
			t.Errorf("rate %v: expected error, got nil", rate)
			continue
		}
		if !strings.Contains(err.Error(), "deviationRate") {
			t.Errorf("rate %v: error %q should mention deviationRate", rate, err)
		}
	}
}
