package obs

import (
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestThresholdExcludesMutated: deliberately-failing mutated requests must not
// produce a threshold (error-rate) finding on an otherwise healthy run.
func TestThresholdExcludesMutated(t *testing.T) {
	a := NewAggregator()
	a.Add(RequestObservation{APIID: "x", StatusCode: 200, LatencyMs: 5})
	for i := 0; i < 10; i++ {
		a.Add(RequestObservation{APIID: "x", StatusCode: 500, Mutated: true, LatencyMs: 5})
	}
	for _, f := range a.Classify("r", ClassifyConfig{ErrorRateThreshold: 0.1}) {
		if f.Category == domain.FindingThreshold {
			t.Fatalf("mutated failures must not yield a threshold finding: %+v", f)
		}
	}
}

// TestPerApiFirstSeen: each per-API finding carries that API's own first-seen
// timestamp, not a single global earliest one.
func TestPerApiFirstSeen(t *testing.T) {
	t1, t2 := time.Unix(100, 0), time.Unix(200, 0)
	a := NewAggregator()
	a.Add(RequestObservation{APIID: "a", StatusCode: 500, TS: t1})
	a.Add(RequestObservation{APIID: "b", StatusCode: 500, TS: t2})

	got := map[domain.ID]time.Time{}
	for _, f := range a.Classify("r", ClassifyConfig{}) {
		if f.Category == domain.FindingContract {
			got[domain.ID(f.EvidenceRef)] = f.FirstSeen
		}
	}
	if !got["a"].Equal(t1) || !got["b"].Equal(t2) {
		t.Fatalf("per-API FirstSeen incorrect: a=%v b=%v", got["a"], got["b"])
	}
}
