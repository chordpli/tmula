package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/mask"
)

// TestRootCauseClassJSONRoundTrip pins the reproduce annotation's wire shape:
// the field round-trips, and a finding that was never reproduced serializes
// without the key, so legacy-shaped findings stay byte-identical.
func TestRootCauseClassJSONRoundTrip(t *testing.T) {
	in := Finding{
		RunID: "r1", Category: FindingContract, Severity: SeverityCritical,
		EvidenceRef: "api-checkout", Description: "d",
		RootCauseClass: RootCauseFunctional,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"rootCauseClass":"functional"`) {
		t.Errorf("annotation missing from wire shape: %s", data)
	}
	var out Finding
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.RootCauseClass != RootCauseFunctional {
		t.Errorf("RootCauseClass = %q, want %q", out.RootCauseClass, RootCauseFunctional)
	}

	// Without the annotation the key must be omitted entirely.
	plain, err := json.Marshal(Finding{RunID: "r1", Category: FindingThreshold, Severity: SeverityWarning, EvidenceRef: "error-rate", Description: "d"})
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if strings.Contains(string(plain), "rootCauseClass") {
		t.Errorf("rootCauseClass key serialized for an unannotated finding: %s", plain)
	}
}

// TestRootCauseClassSurvivesPIIMasking confirms the shared-report masking path
// leaves the annotation intact: "rootCauseClass" must not look sensitive to
// the deny-by-default masker (no sensitive substring, no sensitive token).
func TestRootCauseClassSurvivesPIIMasking(t *testing.T) {
	f := evidenceFixture()
	f.RootCauseClass = RootCauseLoadDependent
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	masked := mask.New(mask.Config{}).MaskJSON(data)
	var out Finding
	if err := json.Unmarshal(masked, &out); err != nil {
		t.Fatalf("unmarshal masked finding: %v", err)
	}
	if out.RootCauseClass != RootCauseLoadDependent {
		t.Errorf("rootCauseClass was masked to %q; it is a verdict label, not PII", out.RootCauseClass)
	}
}
