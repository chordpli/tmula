package scenariofile

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/obs"
)

// TestExpandCarriesFindings wires the compact file's findings block into the
// spec, where the run path resolves it into the classifier's thresholds —
// replacing the control plane's hardcoded 0.2/5 and finally giving the p95
// gate a configuration path.
func TestExpandCarriesFindings(t *testing.T) {
	s, err := Parse([]byte(`
target: http://localhost:9000
findings:
  errorRate: 0.5
  p95LatencyMs: 800
  availabilityStreak: 3
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
	if spec.Findings == nil {
		t.Fatal("expanded spec carries no findings block")
	}
	got := spec.Findings.ClassifyConfig()
	want := obs.ClassifyConfig{ErrorRateThreshold: 0.5, P95LatencyMs: 800, AvailabilityRun: 3}
	if got != want {
		t.Errorf("resolved ClassifyConfig = %+v, want %+v", got, want)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded spec failed validation: %v", err)
	}
}

// TestExpandDefaultsFindingsToNil pins the default: a file that does not
// mention findings expands to a nil block, which resolves to the long-standing
// 0.2 / 5 / p95-disabled thresholds every existing scenario relies on.
func TestExpandDefaultsFindingsToNil(t *testing.T) {
	spec, err := Expand(Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if spec.Findings != nil {
		t.Fatalf("Findings = %+v, want nil (defaults)", spec.Findings)
	}
	if got := spec.Findings.ClassifyConfig(); got != obs.DefaultClassifyConfig() {
		t.Errorf("nil block resolved to %+v, want defaults %+v", got, obs.DefaultClassifyConfig())
	}
}

// TestExpandRejectsBadFindings fails fast on malformed thresholds with a
// scenariofile-prefixed message, instead of deferring to spec validation.
func TestExpandRejectsBadFindings(t *testing.T) {
	cases := []struct {
		name string
		cfg  obs.FindingConfig
		want string // substring the error must mention
	}{
		{"negative errorRate", obs.FindingConfig{ErrorRate: -0.1}, "errorRate"},
		{"errorRate above 1", obs.FindingConfig{ErrorRate: 1.5}, "errorRate"},
		{"negative p95", obs.FindingConfig{P95LatencyMs: -10}, "p95LatencyMs"},
		{"negative streak", obs.FindingConfig{AvailabilityStreak: -2}, "availabilityStreak"},
	}
	for _, tc := range cases {
		cfg := tc.cfg
		_, err := Expand(Scenario{
			Target:   "http://h:1",
			Findings: &cfg,
			Flow:     []Step{{ID: "a", Request: "GET /a"}},
		})
		if err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
			continue
		}
		if !strings.HasPrefix(err.Error(), "scenariofile:") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q should be scenariofile-prefixed and mention %q", tc.name, err, tc.want)
		}
	}
}
