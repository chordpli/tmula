package obs

import (
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

func findingsByCategory(fs []domain.Finding) map[domain.FindingCategory]int {
	m := map[domain.FindingCategory]int{}
	for _, f := range fs {
		m[f.Category]++
	}
	return m
}

func TestClassifyMutation(t *testing.T) {
	a := NewAggregator()
	a.Add(RequestObservation{APIID: "orders", StatusCode: 200})
	a.Add(RequestObservation{APIID: "orders", StatusCode: 400, Mutated: true})
	a.Add(RequestObservation{APIID: "orders", StatusCode: 422, Mutated: true})

	fs := a.Classify("run1", ClassifyConfig{})
	cat := findingsByCategory(fs)
	if cat[domain.FindingMutation] != 1 {
		t.Fatalf("want 1 mutation finding, got %v", cat)
	}
}

func TestClassifyContract(t *testing.T) {
	a := NewAggregator()
	// non-mutated 5xx on the happy path = contract violation.
	a.Add(RequestObservation{APIID: "checkout", StatusCode: 500})
	a.Add(RequestObservation{APIID: "checkout", StatusCode: 200})
	fs := a.Classify("run1", ClassifyConfig{})
	if findingsByCategory(fs)[domain.FindingContract] != 1 {
		t.Fatalf("want 1 contract finding, got %v", fs)
	}
	// A mutated 5xx must NOT be a contract finding (it's expected/mutation).
	b := NewAggregator()
	b.Add(RequestObservation{APIID: "checkout", StatusCode: 500, Mutated: true})
	if findingsByCategory(b.Classify("r", ClassifyConfig{}))[domain.FindingContract] != 0 {
		t.Error("mutated failure should not be a contract finding")
	}
}

func TestClassifyAvailability(t *testing.T) {
	a := NewAggregator()
	for i := 0; i < 5; i++ {
		a.Add(RequestObservation{APIID: "pay", ErrorClass: "timeout"})
	}
	fs := a.Classify("run1", ClassifyConfig{AvailabilityRun: 5})
	if findingsByCategory(fs)[domain.FindingAvailability] != 1 {
		t.Fatalf("5 consecutive timeouts should yield an availability finding, got %v", fs)
	}

	// Four in a row (below threshold) must not.
	b := NewAggregator()
	for i := 0; i < 4; i++ {
		b.Add(RequestObservation{APIID: "pay", StatusCode: 503})
	}
	if findingsByCategory(b.Classify("r", ClassifyConfig{AvailabilityRun: 5}))[domain.FindingAvailability] != 0 {
		t.Error("4 consecutive failures should not trip availability (threshold 5)")
	}
}

func TestAvailabilityResetsOnSuccess(t *testing.T) {
	a := NewAggregator()
	// 3 fail, 1 ok, 3 fail => max run is 3, below threshold 5.
	for i := 0; i < 3; i++ {
		a.Add(RequestObservation{APIID: "x", StatusCode: 500})
	}
	a.Add(RequestObservation{APIID: "x", StatusCode: 200})
	for i := 0; i < 3; i++ {
		a.Add(RequestObservation{APIID: "x", StatusCode: 500})
	}
	fs := a.Classify("r", ClassifyConfig{AvailabilityRun: 5})
	// contract findings appear (non-mutated 5xx), but no availability.
	if findingsByCategory(fs)[domain.FindingAvailability] != 0 {
		t.Error("a success in the middle should reset the consecutive-failure run")
	}
}

// TestClassifyAvailabilityOrderRobust pins TASK 1: availability streaks are
// counted in per-endpoint timestamp order, so the same multiset of observations
// yields the same finding regardless of the arrival order in which interleaved,
// concurrently-streamed results were recorded.
func TestClassifyAvailabilityOrderRobust(t *testing.T) {
	base := time.Unix(1000, 0)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

	// Part 1: a clean 5-long consecutive-failure streak by TS for one API.
	// Build the same set in two different ARRIVAL orders and assert both produce
	// the same availability finding count. ts 1..5 fail, then 6..7 succeed.
	canonical := []RequestObservation{
		{APIID: "pay", ErrorClass: "timeout", TS: at(1)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(2)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(3)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(4)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(5)},
		{APIID: "pay", StatusCode: 200, TS: at(6)},
		{APIID: "pay", StatusCode: 200, TS: at(7)},
	}
	// A deterministic (hand-picked, non-random) permutation of the same slice.
	shuffled := []RequestObservation{
		canonical[6], canonical[2], canonical[0], canonical[5],
		canonical[4], canonical[1], canonical[3],
	}

	sortedAgg := NewAggregator()
	for _, o := range canonical {
		sortedAgg.Add(o)
	}
	shuffledAgg := NewAggregator()
	for _, o := range shuffled {
		shuffledAgg.Add(o)
	}

	cfg := ClassifyConfig{AvailabilityRun: 5}
	gotSorted := findingsByCategory(sortedAgg.Classify("r", cfg))[domain.FindingAvailability]
	gotShuffled := findingsByCategory(shuffledAgg.Classify("r", cfg))[domain.FindingAvailability]
	if gotSorted != 1 {
		t.Fatalf("sorted order: want 1 availability finding, got %d", gotSorted)
	}
	if gotSorted != gotShuffled {
		t.Fatalf("availability finding count must not depend on arrival order: sorted=%d shuffled=%d", gotSorted, gotShuffled)
	}

	// Part 2: arrival order would HIDE the streak, but timestamp order reveals it.
	// The five "down" timestamps for api "pay" are at ts 1..5 (consecutive in
	// time), but in ARRIVAL order each is separated by a later-timestamped success
	// (ts 100+) for the SAME api. Walking by arrival resets the run on every
	// success and never reaches 5; walking by TS sees the contiguous 1..5 streak.
	interleaved := []RequestObservation{
		{APIID: "pay", StatusCode: 200, TS: at(100)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(1)},
		{APIID: "pay", StatusCode: 200, TS: at(101)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(2)},
		{APIID: "pay", StatusCode: 200, TS: at(102)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(3)},
		{APIID: "pay", StatusCode: 200, TS: at(103)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(4)},
		{APIID: "pay", StatusCode: 200, TS: at(104)},
		{APIID: "pay", ErrorClass: "timeout", TS: at(5)},
	}
	ia := NewAggregator()
	for _, o := range interleaved {
		ia.Add(o)
	}
	if got := findingsByCategory(ia.Classify("r", cfg))[domain.FindingAvailability]; got != 1 {
		t.Fatalf("timestamp order should reveal the 5-long streak hidden by arrival interleave, got %d availability findings", got)
	}
}

func TestClassifyThreshold(t *testing.T) {
	a := NewAggregator()
	a.Add(RequestObservation{APIID: "x", StatusCode: 200, LatencyMs: 10})
	a.Add(RequestObservation{APIID: "x", StatusCode: 400, LatencyMs: 10}) // error
	fs := a.Classify("r", ClassifyConfig{ErrorRateThreshold: 0.4})
	if findingsByCategory(fs)[domain.FindingThreshold] != 1 {
		t.Fatalf("error rate 0.5 > 0.4 should yield a threshold finding, got %v", fs)
	}

	// p95 latency breach.
	b := NewAggregator()
	for i := 0; i < 100; i++ {
		b.Add(RequestObservation{APIID: "x", StatusCode: 200, LatencyMs: float64(i)})
	}
	tf := b.Classify("r", ClassifyConfig{P95LatencyMs: 50})
	if findingsByCategory(tf)[domain.FindingThreshold] == 0 {
		t.Error("p95 ~95ms > 50ms should yield a threshold finding")
	}
}

// TestThresholdFindingEvidenceRefs pins the diff identity of threshold
// findings: each carries a stable, non-empty metric-identity evidence ref so
// the error-rate and p95 findings never collide with each other (the run
// comparison keys findings by category + evidence ref).
func TestThresholdFindingEvidenceRefs(t *testing.T) {
	a := NewAggregator()
	for i := 0; i < 10; i++ {
		a.Add(RequestObservation{APIID: "x", StatusCode: 200, LatencyMs: 100})
	}
	a.Add(RequestObservation{APIID: "x", StatusCode: 500, LatencyMs: 100})

	fs := a.Classify("r", ClassifyConfig{ErrorRateThreshold: 0.05, P95LatencyMs: 50})
	refs := map[string]bool{}
	for _, f := range fs {
		if f.Category != domain.FindingThreshold {
			continue
		}
		if f.EvidenceRef == "" {
			t.Fatalf("threshold finding has an empty evidence ref: %+v", f)
		}
		refs[f.EvidenceRef] = true
	}
	if !refs["error-rate"] || !refs["p95-latency"] {
		t.Fatalf("want metric-identity refs %q and %q, got %v", "error-rate", "p95-latency", refs)
	}
}

// TestEmptyAPIIDProducesNoEmptyEvidenceRef pins the non-empty-EvidenceRef
// invariant for the per-API classifiers: an observation with an empty APIID
// (a walk/setup failure that never reached an endpoint) must not yield a
// per-API finding carrying an empty evidence ref — the per-API classifiers
// skip it. Any finding it does contribute to (e.g. the run-wide threshold)
// must still carry a non-empty ref, so the run comparison's (category,
// evidenceRef) key never collapses distinct issues.
func TestEmptyAPIIDProducesNoEmptyEvidenceRef(t *testing.T) {
	a := NewAggregator()
	// A walk-construction failure: empty APIID, non-empty ErrorClass, no status.
	a.Add(RequestObservation{APIID: "", ErrorClass: "transport"})

	// Drive every per-API classifier at once (mutation/contract/availability via
	// AvailabilityRun=1) plus the threshold classifier.
	fs := a.Classify("r", ClassifyConfig{ErrorRateThreshold: 0.1, AvailabilityRun: 1})
	for _, f := range fs {
		if f.EvidenceRef == "" {
			t.Fatalf("finding has an empty evidence ref: %+v", f)
		}
	}

	// And specifically: the empty-APIID observation produced no per-API finding.
	cat := findingsByCategory(fs)
	for _, c := range []domain.FindingCategory{domain.FindingMutation, domain.FindingContract, domain.FindingAvailability} {
		if cat[c] != 0 {
			t.Fatalf("empty APIID should not produce a %v finding, got %d", c, cat[c])
		}
	}
}

// TestFindingCountField: per-API findings expose the occurrence count as a
// structured field matching the number formatted into the description.
func TestFindingCountField(t *testing.T) {
	a := NewAggregator()
	for i := 0; i < 3; i++ {
		a.Add(RequestObservation{APIID: "checkout", StatusCode: 500})
	}
	for _, f := range a.Classify("r", ClassifyConfig{}) {
		if f.Category == domain.FindingContract {
			if f.Count != 3 {
				t.Fatalf("contract finding Count = %d, want 3", f.Count)
			}
			return
		}
	}
	t.Fatal("no contract finding produced")
}

func TestNoFindingsOnCleanRun(t *testing.T) {
	a := NewAggregator()
	for i := 0; i < 50; i++ {
		a.Add(RequestObservation{APIID: "x", StatusCode: 200, LatencyMs: 5})
	}
	fs := a.Classify("r", ClassifyConfig{ErrorRateThreshold: 0.1, P95LatencyMs: 100, AvailabilityRun: 5})
	if len(fs) != 0 {
		t.Fatalf("clean run should have no findings, got %v", fs)
	}
}

func TestPerApiGrouping(t *testing.T) {
	a := NewAggregator()
	for i := 0; i < 10; i++ {
		a.Add(RequestObservation{APIID: "a", StatusCode: 500})
		a.Add(RequestObservation{APIID: "b", StatusCode: 500})
	}
	fs := a.Classify("r", ClassifyConfig{})
	// One contract finding per API, not one per request.
	if findingsByCategory(fs)[domain.FindingContract] != 2 {
		t.Fatalf("want 2 contract findings (one per API), got %d", findingsByCategory(fs)[domain.FindingContract])
	}
}
