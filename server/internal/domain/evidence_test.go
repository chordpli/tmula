package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/mask"
)

// evidenceFixture builds a fully-populated finding so the round-trip tests
// exercise every evidence field at once.
func evidenceFixture() Finding {
	ts := time.Date(2026, 6, 11, 10, 30, 0, 0, time.UTC)
	return Finding{
		RunID:       "r1",
		Category:    FindingContract,
		Severity:    SeverityCritical,
		EvidenceRef: "api-checkout",
		FirstSeen:   ts,
		Description: "3 contract violation(s) on api-checkout",
		Count:       3,
		Evidence: &FindingEvidence{
			Sessions: []EvidenceSession{{
				SessionID:  "vu-browser-s42",
				Seed:       42,
				UserIndex:  41,
				Persona:    "browser",
				Path:       []ID{"home", "search", "checkout"},
				StatusCode: 503,
				LatencyMs:  812.5,
				ErrorClass: "transport",
				TS:         ts,
			}},
			TimeBuckets: []EvidenceBucket{
				{Label: "0-25%", Count: 0},
				{Label: "25-50%", Count: 1},
				{Label: "50-75%", Count: 0},
				{Label: "75-100%", Count: 2},
			},
			StatusCounts: map[int]int{503: 2, 500: 1},
		},
	}
}

// TestFindingEvidenceJSONRoundTrip pins that a finding's evidence bundle
// survives JSON marshal/unmarshal byte-for-byte equivalent — the exact body
// shape the Postgres store persists and the MemStore snapshots.
func TestFindingEvidenceJSONRoundTrip(t *testing.T) {
	in := evidenceFixture()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Finding
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Evidence == nil {
		t.Fatal("evidence dropped in round trip")
	}
	if len(out.Evidence.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(out.Evidence.Sessions))
	}
	s := out.Evidence.Sessions[0]
	want := in.Evidence.Sessions[0]
	if s.SessionID != want.SessionID || s.Seed != want.Seed || s.UserIndex != want.UserIndex ||
		s.Persona != want.Persona || s.StatusCode != want.StatusCode ||
		s.LatencyMs != want.LatencyMs || s.ErrorClass != want.ErrorClass || !s.TS.Equal(want.TS) {
		t.Errorf("session round trip mismatch: got %+v want %+v", s, want)
	}
	if len(s.Path) != 3 || s.Path[2] != "checkout" {
		t.Errorf("path round trip mismatch: %v", s.Path)
	}
	if len(out.Evidence.TimeBuckets) != 4 || out.Evidence.TimeBuckets[3].Count != 2 {
		t.Errorf("time buckets round trip mismatch: %+v", out.Evidence.TimeBuckets)
	}
	if out.Evidence.StatusCounts[503] != 2 || out.Evidence.StatusCounts[500] != 1 {
		t.Errorf("status counts round trip mismatch: %+v", out.Evidence.StatusCounts)
	}
}

// TestFindingWithoutEvidenceStaysCompact pins backward/forward compatibility:
// a finding without evidence serializes without the key (so old readers see
// the exact shape they always did), and pre-evidence JSON deserializes into
// the new struct with a nil Evidence.
func TestFindingWithoutEvidenceStaysCompact(t *testing.T) {
	data, err := json.Marshal(Finding{RunID: "r1", Category: FindingThreshold, Severity: SeverityWarning, EvidenceRef: "error-rate", Description: "d"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"evidence":`) {
		t.Errorf("evidence key serialized for a finding without one: %s", data)
	}

	// Old persisted JSON (no evidence key) must load cleanly.
	old := `{"runId":"r1","category":"threshold","severity":"warning","evidenceRef":"error-rate","firstSeen":"0001-01-01T00:00:00Z","description":"d"}`
	var f Finding
	if err := json.Unmarshal([]byte(old), &f); err != nil {
		t.Fatalf("unmarshal legacy finding: %v", err)
	}
	if f.Evidence != nil {
		t.Errorf("legacy finding grew evidence: %+v", f.Evidence)
	}
}

// TestEvidenceSurvivesPIIMasking confirms the shared-report masking path
// (api/share.go marshals the report and runs mask.MaskJSON over it) leaves
// the evidence intact: virtual-user session labels and node ids are synthetic
// tmula identifiers, not PII, so the deny-by-default masker — which redacts
// any field whose NAME looks sensitive, including the substring "session" —
// must not see a sensitive-looking field name in the evidence wire shape.
func TestEvidenceSurvivesPIIMasking(t *testing.T) {
	data, err := json.Marshal(evidenceFixture())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	masked := mask.New(mask.Config{}).MaskJSON(data)
	var f Finding
	if err := json.Unmarshal(masked, &f); err != nil {
		t.Fatalf("unmarshal masked finding: %v", err)
	}
	if f.Evidence == nil || len(f.Evidence.Sessions) != 1 {
		t.Fatalf("masking dropped the evidence bundle: %s", masked)
	}
	s := f.Evidence.Sessions[0]
	if s.SessionID != "vu-browser-s42" {
		t.Errorf("session id was masked to %q; it is a synthetic id, not PII", s.SessionID)
	}
	if len(s.Path) != 3 || s.Path[0] != "home" {
		t.Errorf("path was masked to %v; node ids are not PII", s.Path)
	}
	if s.Seed != 42 || s.UserIndex != 41 {
		t.Errorf("reproduce coordinates were masked: seed=%d userIndex=%d", s.Seed, s.UserIndex)
	}
}
