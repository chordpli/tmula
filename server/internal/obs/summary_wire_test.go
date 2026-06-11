package obs

import (
	"reflect"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// sampleSummary builds a summary with a known mix: 8 fast successes and 2 slow
// 5xx failures, so total=10, errors=2 (errorRate 0.2), and the failures trip the
// contract/availability/threshold signal tallies.
func sampleSummary() *Summary {
	s := NewSummary()
	for i := 0; i < 8; i++ {
		s.Add(RequestObservation{APIID: "a", StatusCode: 200, LatencyMs: 10})
	}
	for i := 0; i < 2; i++ {
		s.Add(RequestObservation{APIID: "a", StatusCode: 500, LatencyMs: 50})
	}
	return s
}

func TestSummaryWireRoundtrip(t *testing.T) {
	orig := sampleSummary()
	reloaded, err := LoadSummary(orig.Export())
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if !reflect.DeepEqual(orig.Stats(), reloaded.Stats()) {
		t.Errorf("stats differ after roundtrip:\n orig=%+v\n got =%+v", orig.Stats(), reloaded.Stats())
	}
	if !reflect.DeepEqual(orig.FindingCounts(), reloaded.FindingCounts()) {
		t.Errorf("finding counts differ: orig=%v got=%v", orig.FindingCounts(), reloaded.FindingCounts())
	}
}

// TestSummaryMergeViaWire checks that exporting+reloading is transparent to
// Merge: merging two reloaded summaries equals merging the originals directly.
func TestSummaryMergeViaWire(t *testing.T) {
	a, b := sampleSummary(), sampleSummary()

	direct := NewSummary()
	direct.Merge(a)
	direct.Merge(b)

	ra, err := LoadSummary(a.Export())
	if err != nil {
		t.Fatalf("load a: %v", err)
	}
	rb, err := LoadSummary(b.Export())
	if err != nil {
		t.Fatalf("load b: %v", err)
	}
	viaWire := NewSummary()
	viaWire.Merge(ra)
	viaWire.Merge(rb)

	if !reflect.DeepEqual(direct.Stats(), viaWire.Stats()) {
		t.Errorf("merged stats differ:\n direct=%+v\n wire  =%+v", direct.Stats(), viaWire.Stats())
	}
	if got := viaWire.Stats().Total; got != 20 {
		t.Errorf("merged total = %d, want 20", got)
	}
}

func TestLoadHistogramRejectsBadLength(t *testing.T) {
	if _, err := LoadHistogram([]uint64{1, 2, 3}, 5); err == nil {
		t.Error("expected an error for a wrong-length bucket slice")
	}
	if _, err := LoadHistogram(make([]uint64, NumBuckets()), 5); err != nil {
		t.Errorf("correct length should load: %v", err)
	}
}

func TestFindingsFromSummary(t *testing.T) {
	s := sampleSummary() // errorRate 0.2, 2 contract/availability signals
	cfg := ClassifyConfig{ErrorRateThreshold: 0.1, AvailabilityRun: 2}
	got := map[domain.FindingCategory]bool{}
	for _, f := range FindingsFromSummary("run-1", s, cfg) {
		got[f.Category] = true
	}
	for _, want := range []domain.FindingCategory{domain.FindingContract, domain.FindingAvailability, domain.FindingThreshold} {
		if !got[want] {
			t.Errorf("expected a %q finding, got categories %v", want, got)
		}
	}
	if got[domain.FindingMutation] {
		t.Error("did not expect a mutation finding (no mutated inputs)")
	}
}

// TestFindingsFromSummaryEvidenceRefs: every summary-derived finding carries a
// stable, non-empty evidence ref — "run-wide" for the coarse per-category
// findings (a Summary has no per-API breakdown) and the metric identity for
// threshold findings — so the run-comparison diff can key them reliably.
func TestFindingsFromSummaryEvidenceRefs(t *testing.T) {
	s := sampleSummary()
	cfg := ClassifyConfig{ErrorRateThreshold: 0.1, AvailabilityRun: 2}
	for _, f := range FindingsFromSummary("run-1", s, cfg) {
		if f.EvidenceRef == "" {
			t.Errorf("%s finding has an empty evidence ref: %+v", f.Category, f)
		}
		if f.Category != domain.FindingThreshold && f.EvidenceRef != "run-wide" {
			t.Errorf("%s finding evidence ref = %q, want %q", f.Category, f.EvidenceRef, "run-wide")
		}
	}
}
