package workload

import (
	"errors"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/obs"
)

// TestRecordCarriesSessionContext: the open-model recording path forwards a
// session's identity, reproduce coordinates (seed and arrival offset),
// persona and failure path into the observation, so findings classified from
// an open run carry the same evidence the closed path produces.
func TestRecordCarriesSessionContext(t *testing.T) {
	collector := obs.NewCollector()
	agg := obs.NewAggregator()
	now := func() time.Time { return time.Unix(1000, 0) }

	const runSeed = int64(500)
	const sessionSeed = runSeed + 3 // arrival #3
	results := []load.StepResult{
		{UserID: "vu-browser-s503", NodeID: "a", Resp: load.Response{StatusCode: 200, LatencyMs: 5}, Seed: sessionSeed},
		{
			UserID: "vu-browser-s503", NodeID: "b", Seed: sessionSeed,
			Resp: load.Response{StatusCode: 0, LatencyMs: 30},
			Err:  errors.New("connection refused"),
			Path: []domain.ID{"a", "b"},
		},
	}
	record(collector, agg, results, "browser", runSeed, now)

	// A transport failure is an error-rate signal (not a contract one), so the
	// run-wide threshold finding is where this session's evidence must land.
	fs := agg.Classify("r", obs.ClassifyConfig{ErrorRateThreshold: 0.4})
	var found *domain.Finding
	for i := range fs {
		if fs[i].Category == domain.FindingThreshold && fs[i].EvidenceRef == "error-rate" {
			found = &fs[i]
		}
	}
	if found == nil {
		t.Fatalf("no error-rate threshold finding in %+v", fs)
	}
	if found.Evidence == nil || len(found.Evidence.Sessions) != 1 {
		t.Fatalf("evidence = %+v, want one representative session", found.Evidence)
	}
	s := found.Evidence.Sessions[0]
	if s.SessionID != "vu-browser-s503" {
		t.Errorf("session id = %q", s.SessionID)
	}
	if s.Seed != sessionSeed || s.UserIndex != 3 {
		t.Errorf("reproduce coordinates = seed %d / index %d, want %d / 3", s.Seed, s.UserIndex, sessionSeed)
	}
	if s.Persona != "browser" {
		t.Errorf("persona = %q, want browser", s.Persona)
	}
	if len(s.Path) != 2 || s.Path[1] != "b" {
		t.Errorf("path = %v, want [a b]", s.Path)
	}
	if s.ErrorClass != "transport" {
		t.Errorf("error class = %q, want transport", s.ErrorClass)
	}
}
