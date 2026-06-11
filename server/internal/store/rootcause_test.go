package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestRootCauseClassSurvivesPersistence pins that the reproduce verdict a
// finding is annotated with survives both persistence paths: the MemStore's
// JSON file snapshot (the local engine's restart story) and the body-JSON
// round trip the Postgres store uses for a finding row.
func TestRootCauseClassSurvivesPersistence(t *testing.T) {
	f := evidenceFinding("r1")
	f.RootCauseClass = domain.RootCauseFlaky

	// MemStore snapshot save/load.
	path := filepath.Join(t.TempDir(), "snap.json")
	src := NewMemStore()
	if err := src.SaveFindings("r1", []domain.Finding{f}); err != nil {
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
	if len(got) != 1 || got[0].RootCauseClass != domain.RootCauseFlaky {
		t.Errorf("snapshot round trip lost the annotation: %+v", got)
	}

	// Postgres body-JSON round trip (same serialization as the jsonb column).
	body, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var out domain.Finding
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if out.RootCauseClass != domain.RootCauseFlaky {
		t.Errorf("body round trip lost the annotation: %+v", out)
	}
}
