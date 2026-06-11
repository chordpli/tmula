package obs

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestDefaultClassifyConfig pins the package defaults every run classified
// with before the findings block existed: error rate 0.2, availability run 5,
// p95 gate disabled. Changing any of these silently re-grades historical runs,
// so the values are asserted explicitly.
func TestDefaultClassifyConfig(t *testing.T) {
	want := ClassifyConfig{ErrorRateThreshold: 0.2, P95LatencyMs: 0, AvailabilityRun: 5}
	if got := DefaultClassifyConfig(); got != want {
		t.Errorf("DefaultClassifyConfig() = %+v, want %+v", got, want)
	}
}

// TestFindingConfigNilFallsBackToDefaults: a spec without a findings block (a
// nil config) resolves to exactly the defaults, so every existing spec
// classifies as before.
func TestFindingConfigNilFallsBackToDefaults(t *testing.T) {
	var c *FindingConfig
	if got := c.ClassifyConfig(); got != DefaultClassifyConfig() {
		t.Errorf("nil config = %+v, want defaults %+v", got, DefaultClassifyConfig())
	}
}

// TestFindingConfigPartialOverride: a block that sets only some fields keeps
// the defaults for the rest — enabling the p95 gate must not silently drop the
// error-rate or availability gates.
func TestFindingConfigPartialOverride(t *testing.T) {
	got := (&FindingConfig{P95LatencyMs: 800}).ClassifyConfig()
	want := ClassifyConfig{ErrorRateThreshold: 0.2, P95LatencyMs: 800, AvailabilityRun: 5}
	if got != want {
		t.Errorf("partial override = %+v, want %+v", got, want)
	}
}

// TestFindingConfigFullOverride: every configured field reaches the resolved
// ClassifyConfig.
func TestFindingConfigFullOverride(t *testing.T) {
	got := (&FindingConfig{ErrorRate: 0.5, P95LatencyMs: 250, AvailabilityStreak: 3}).ClassifyConfig()
	want := ClassifyConfig{ErrorRateThreshold: 0.5, P95LatencyMs: 250, AvailabilityRun: 3}
	if got != want {
		t.Errorf("full override = %+v, want %+v", got, want)
	}
}

// TestFindingConfigValidate rejects thresholds that cannot classify anything
// sensibly (a rate outside [0,1], a negative latency or streak) and accepts
// zero values, which mean "use the default".
func TestFindingConfigValidate(t *testing.T) {
	bad := []struct {
		name string
		cfg  FindingConfig
		want string // substring the error must mention
	}{
		{"negative errorRate", FindingConfig{ErrorRate: -0.1}, "errorRate"},
		{"errorRate above 1", FindingConfig{ErrorRate: 1.5}, "errorRate"},
		{"negative p95", FindingConfig{P95LatencyMs: -10}, "p95LatencyMs"},
		{"negative streak", FindingConfig{AvailabilityStreak: -1}, "availabilityStreak"},
	}
	for _, tc := range bad {
		err := tc.cfg.Validate()
		if err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q should mention %q", tc.name, err, tc.want)
		}
	}

	good := []*FindingConfig{
		nil,
		{},
		{ErrorRate: 1},
		{ErrorRate: 0.05, P95LatencyMs: 100, AvailabilityStreak: 2},
	}
	for _, c := range good {
		if err := c.Validate(); err != nil {
			t.Errorf("config %+v: unexpected error %v", c, err)
		}
	}
}

// TestFindingConfigEnablesP95Finding drives the previously-dead p95 gate end
// to end through the aggregator: a configured p95 threshold produces a
// threshold finding carrying the stable "p95-latency" evidence ref the run
// comparison keys on.
func TestFindingConfigEnablesP95Finding(t *testing.T) {
	a := NewAggregator()
	for i := 0; i < 100; i++ {
		a.Add(RequestObservation{APIID: "x", StatusCode: 200, LatencyMs: float64(i)})
	}
	fs := a.Classify("r", (&FindingConfig{P95LatencyMs: 50}).ClassifyConfig())
	for _, f := range fs {
		if f.Category == domain.FindingThreshold && f.EvidenceRef == "p95-latency" {
			return
		}
	}
	t.Fatalf("configured p95 gate produced no p95-latency threshold finding, got %v", fs)
}
