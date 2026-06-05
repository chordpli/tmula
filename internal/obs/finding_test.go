package obs

import (
	"testing"

	"github.com/chordpli/tmula/internal/domain"
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
