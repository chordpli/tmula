package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// evidenceFinding builds a finding with a fully-populated evidence bundle for
// the persistence round-trip tests.
func evidenceFinding(runID domain.ID) domain.Finding {
	ts := time.Date(2026, 6, 11, 10, 30, 0, 0, time.UTC)
	return domain.Finding{
		RunID:       runID,
		Category:    domain.FindingContract,
		Severity:    domain.SeverityCritical,
		EvidenceRef: "api-checkout",
		FirstSeen:   ts,
		Description: "2 contract violation(s) on api-checkout",
		Count:       2,
		Evidence: &domain.FindingEvidence{
			Sessions: []domain.EvidenceSession{{
				SessionID:  "vu-browser-s42",
				Seed:       42,
				UserIndex:  41,
				Persona:    "browser",
				Path:       []domain.ID{"home", "search", "checkout"},
				StatusCode: 503,
				LatencyMs:  812.5,
				ErrorClass: "transport",
				TS:         ts,
			}},
			TimeBuckets: []domain.EvidenceBucket{
				{Label: "0-25%", Count: 1},
				{Label: "25-50%", Count: 0},
				{Label: "50-75%", Count: 0},
				{Label: "75-100%", Count: 1},
			},
			StatusCounts: map[int]int{503: 2},
		},
	}
}

// TestMemStoreSnapshotRoundTripsEvidence: findings with evidence bundles
// survive the MemStore's JSON file snapshot save/load — the persistence the
// local engine restarts from.
func TestMemStoreSnapshotRoundTripsEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	src := NewMemStore()
	want := []domain.Finding{evidenceFinding("r1")}
	if err := src.SaveFindings("r1", want); err != nil {
		t.Fatalf("save findings: %v", err)
	}
	if err := src.SaveToFile(path); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	dst := NewMemStore()
	if err := dst.LoadFromFile(path); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	got, err := dst.Findings("r1")
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(got) != 1 || got[0].Evidence == nil {
		t.Fatalf("evidence lost in snapshot round trip: %+v", got)
	}
	assertSameJSON(t, got, want)
}

// assertSameJSON compares two values by their canonical JSON encoding, which
// sidesteps time.Time's internal representation differing across a
// marshal/unmarshal round trip while still pinning every serialized field.
func assertSameJSON(t *testing.T, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(g) != string(w) {
		t.Errorf("round trip mismatch:\n got %s\nwant %s", g, w)
	}
}

// TestPostgresBodyJSONRoundTripsEvidence pins the exact serialization the
// Postgres store uses for a finding row: SaveFindings marshals each
// domain.Finding into the body jsonb column and Findings unmarshals it back,
// so a plain marshal/unmarshal round trip of the same value proves evidence
// survives that path without needing a live database (the live integration
// test in postgres_test.go remains opt-in via TMULA_TEST_POSTGRES).
func TestPostgresBodyJSONRoundTripsEvidence(t *testing.T) {
	want := evidenceFinding("r1")
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var got domain.Finding
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Evidence == nil {
		t.Fatal("evidence lost in body round trip")
	}
	assertSameJSON(t, got, want)
}
