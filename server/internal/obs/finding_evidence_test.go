package obs

import (
	"fmt"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// findingFor returns the first finding matching (category, evidenceRef), or
// fails the test — the lookup every evidence test below starts from.
func findingFor(t *testing.T, fs []domain.Finding, cat domain.FindingCategory, ref string) domain.Finding {
	t.Helper()
	for _, f := range fs {
		if f.Category == cat && f.EvidenceRef == ref {
			return f
		}
	}
	t.Fatalf("no %v finding with ref %q in %+v", cat, ref, fs)
	return domain.Finding{}
}

// TestClassifyAttachesContractEvidence: a contract finding carries the
// diagnostic bundle for ITS endpoint only — representative sessions with
// reproduce coordinates and the path walked to the failure, plus the status
// distribution — and never evidence from another API's failures.
func TestClassifyAttachesContractEvidence(t *testing.T) {
	base := time.Unix(1000, 0)
	a := NewAggregator()
	// Two failing sessions on api-a, one on api-b, one healthy request.
	a.Add(RequestObservation{
		APIID: "api-a", StatusCode: 500, LatencyMs: 120, TS: base.Add(1 * time.Second),
		SessionID: "vu-s11", Seed: 11, UserIndex: 10, Persona: "browser",
		Path: []domain.ID{"home", "search", "api-a"},
	})
	a.Add(RequestObservation{
		APIID: "api-a", StatusCode: 503, LatencyMs: 80, TS: base.Add(2 * time.Second),
		SessionID: "vu-s12", Seed: 12, UserIndex: 11,
		Path: []domain.ID{"home", "api-a"},
	})
	a.Add(RequestObservation{
		APIID: "api-b", StatusCode: 500, LatencyMs: 50, TS: base.Add(3 * time.Second),
		SessionID: "vu-s13", Seed: 13, UserIndex: 12,
		Path: []domain.ID{"home", "api-b"},
	})
	a.Add(RequestObservation{APIID: "api-a", StatusCode: 200, LatencyMs: 10, TS: base, SessionID: "vu-s14", Seed: 14, UserIndex: 13})

	fs := a.Classify("r", ClassifyConfig{})
	f := findingFor(t, fs, domain.FindingContract, "api-a")
	if f.Evidence == nil {
		t.Fatal("contract finding has no evidence")
	}
	ev := f.Evidence
	if len(ev.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 (one per failing session on api-a): %+v", len(ev.Sessions), ev.Sessions)
	}
	// Earliest failure first.
	s := ev.Sessions[0]
	if s.SessionID != "vu-s11" || s.Seed != 11 || s.UserIndex != 10 || s.Persona != "browser" {
		t.Errorf("first session = %+v, want vu-s11 with its reproduce coordinates and persona", s)
	}
	if len(s.Path) != 3 || s.Path[2] != "api-a" {
		t.Errorf("first session path = %v, want the walk up to the failure", s.Path)
	}
	if s.StatusCode != 500 || s.ErrorClass != "" || !s.TS.Equal(base.Add(1*time.Second)) || s.LatencyMs != 120 {
		t.Errorf("first session failing request = %+v", s)
	}
	for _, es := range ev.Sessions {
		if es.SessionID == "vu-s13" {
			t.Error("evidence leaked a session that failed on a different API")
		}
	}
	// Status distribution covers ONLY api-a's contract failures.
	if ev.StatusCounts[500] != 1 || ev.StatusCounts[503] != 1 || len(ev.StatusCounts) != 2 {
		t.Errorf("status counts = %+v, want {500:1, 503:1}", ev.StatusCounts)
	}
}

// TestEvidenceSessionCapAndRepresentatives: with more failing sessions than
// the cap, the bundle keeps maxEvidenceSessions representatives chosen for
// diagnosability — the earliest failures (ramp-up) plus the slowest of the
// rest — instead of the first N in arrival order.
func TestEvidenceSessionCapAndRepresentatives(t *testing.T) {
	base := time.Unix(1000, 0)
	a := NewAggregator()
	// Ten failing sessions, in time order s0..s9. Latency peaks at s7 (900ms)
	// and s5 (800ms); the others stay low.
	latency := []float64{10, 20, 30, 40, 50, 800, 60, 900, 70, 80}
	for i := 0; i < 10; i++ {
		a.Add(RequestObservation{
			APIID: "api-a", StatusCode: 500, LatencyMs: latency[i], TS: base.Add(time.Duration(i) * time.Second),
			SessionID: fmt.Sprintf("vu-s%d", i), Seed: int64(100 + i), UserIndex: int64(i),
		})
	}

	fs := a.Classify("r", ClassifyConfig{})
	ev := findingFor(t, fs, domain.FindingContract, "api-a").Evidence
	if ev == nil {
		t.Fatal("no evidence")
	}
	if len(ev.Sessions) != maxEvidenceSessions {
		t.Fatalf("sessions = %d, want cap %d", len(ev.Sessions), maxEvidenceSessions)
	}
	got := make([]string, len(ev.Sessions))
	for i, s := range ev.Sessions {
		got[i] = s.SessionID
	}
	// Three earliest (s0, s1, s2), then the two slowest of the rest (s7 900ms,
	// s5 800ms).
	want := []string{"vu-s0", "vu-s1", "vu-s2", "vu-s7", "vu-s5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("representatives = %v, want %v", got, want)
		}
	}
	// The distribution still counts every failure, not just the representatives.
	if ev.StatusCounts[500] != 10 {
		t.Errorf("status counts = %+v, want all 10 failures", ev.StatusCounts)
	}
}

// TestEvidenceDedupesSessions: a session failing many times appears once in
// the representatives (its earliest failure), while the distributions keep
// counting every failure.
func TestEvidenceDedupesSessions(t *testing.T) {
	base := time.Unix(1000, 0)
	a := NewAggregator()
	for i := 0; i < 4; i++ {
		a.Add(RequestObservation{
			APIID: "api-a", StatusCode: 500, LatencyMs: float64(10 * (i + 1)), TS: base.Add(time.Duration(i) * time.Second),
			SessionID: "vu-s1", Seed: 1, UserIndex: 0,
		})
	}

	fs := a.Classify("r", ClassifyConfig{})
	ev := findingFor(t, fs, domain.FindingContract, "api-a").Evidence
	if ev == nil {
		t.Fatal("no evidence")
	}
	if len(ev.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 (deduped)", len(ev.Sessions))
	}
	if !ev.Sessions[0].TS.Equal(base) {
		t.Errorf("kept failure at %v, want the session's earliest", ev.Sessions[0].TS)
	}
	if ev.StatusCounts[500] != 4 {
		t.Errorf("status counts = %+v, want all 4 failures", ev.StatusCounts)
	}
}

// TestEvidenceTimeBuckets: failures are bucketed into four fixed quarters of
// the observed run window (earliest..latest observation), so the report can
// show whether they cluster early in ramp-up or late in soak.
func TestEvidenceTimeBuckets(t *testing.T) {
	base := time.Unix(1000, 0)
	a := NewAggregator()
	// Healthy requests pin the run window to [base, base+100s].
	a.Add(RequestObservation{APIID: "api-a", StatusCode: 200, TS: base})
	a.Add(RequestObservation{APIID: "api-a", StatusCode: 200, TS: base.Add(100 * time.Second)})
	// Failures at 10s (first quarter), 60s (third), 90s and 95s (fourth).
	for _, sec := range []int{10, 60, 90, 95} {
		a.Add(RequestObservation{APIID: "api-a", StatusCode: 500, TS: base.Add(time.Duration(sec) * time.Second)})
	}

	fs := a.Classify("r", ClassifyConfig{})
	ev := findingFor(t, fs, domain.FindingContract, "api-a").Evidence
	if ev == nil {
		t.Fatal("no evidence")
	}
	if len(ev.TimeBuckets) != 4 {
		t.Fatalf("buckets = %d, want 4 fixed quarters: %+v", len(ev.TimeBuckets), ev.TimeBuckets)
	}
	wantLabels := []string{"0-25%", "25-50%", "50-75%", "75-100%"}
	wantCounts := []int{1, 0, 1, 2}
	for i, b := range ev.TimeBuckets {
		if b.Label != wantLabels[i] || b.Count != wantCounts[i] {
			t.Errorf("bucket[%d] = %+v, want {%s %d}", i, b, wantLabels[i], wantCounts[i])
		}
	}
}

// TestThresholdErrorRateEvidence: the run-wide error-rate finding carries
// evidence drawn from every non-mutated failure regardless of endpoint.
func TestThresholdErrorRateEvidence(t *testing.T) {
	base := time.Unix(1000, 0)
	a := NewAggregator()
	a.Add(RequestObservation{APIID: "api-a", StatusCode: 500, TS: base, SessionID: "vu-s1", Seed: 1})
	a.Add(RequestObservation{APIID: "api-b", ErrorClass: "transport", TS: base.Add(time.Second), SessionID: "vu-s2", Seed: 2})
	a.Add(RequestObservation{APIID: "api-a", StatusCode: 200, TS: base.Add(2 * time.Second), SessionID: "vu-s3", Seed: 3})

	fs := a.Classify("r", ClassifyConfig{ErrorRateThreshold: 0.5})
	ev := findingFor(t, fs, domain.FindingThreshold, "error-rate").Evidence
	if ev == nil {
		t.Fatal("error-rate finding has no evidence")
	}
	if len(ev.Sessions) != 2 {
		t.Fatalf("sessions = %d, want the 2 failing sessions across APIs: %+v", len(ev.Sessions), ev.Sessions)
	}
	if ev.Sessions[1].ErrorClass != "transport" {
		t.Errorf("transport failure not carried: %+v", ev.Sessions[1])
	}
	// Only real HTTP codes enter the distribution; a transport error has none.
	if ev.StatusCounts[500] != 1 || len(ev.StatusCounts) != 1 {
		t.Errorf("status counts = %+v, want {500:1}", ev.StatusCounts)
	}
}

// TestThresholdP95Evidence: the p95-latency finding's evidence points at the
// requests that actually breached the gate — the slow ones — even when they
// succeeded, since slowness (not failure) is this finding's signal.
func TestThresholdP95Evidence(t *testing.T) {
	base := time.Unix(1000, 0)
	a := NewAggregator()
	// 10 fast + 1 slow: rank ceil(0.95*11) = 11 lands on the slow sample, so
	// the p95 gate trips.
	for i := 0; i < 10; i++ {
		a.Add(RequestObservation{APIID: "api-a", StatusCode: 200, LatencyMs: 10, TS: base.Add(time.Duration(i) * time.Second), SessionID: fmt.Sprintf("vu-f%d", i), Seed: int64(i)})
	}
	a.Add(RequestObservation{APIID: "api-a", StatusCode: 200, LatencyMs: 5000, TS: base.Add(30 * time.Second), SessionID: "vu-slow", Seed: 99, UserIndex: 98})

	fs := a.Classify("r", ClassifyConfig{P95LatencyMs: 100})
	ev := findingFor(t, fs, domain.FindingThreshold, "p95-latency").Evidence
	if ev == nil {
		t.Fatal("p95 finding has no evidence")
	}
	if len(ev.Sessions) != 1 || ev.Sessions[0].SessionID != "vu-slow" {
		t.Fatalf("sessions = %+v, want only the request over the gate", ev.Sessions)
	}
	if ev.Sessions[0].LatencyMs != 5000 {
		t.Errorf("latency = %v, want 5000", ev.Sessions[0].LatencyMs)
	}
}

// TestAvailabilityEvidence: an availability finding carries the unavailable
// responses behind the streak.
func TestAvailabilityEvidence(t *testing.T) {
	base := time.Unix(1000, 0)
	a := NewAggregator()
	for i := 0; i < 5; i++ {
		a.Add(RequestObservation{
			APIID: "api-a", StatusCode: 503, TS: base.Add(time.Duration(i) * time.Second),
			SessionID: fmt.Sprintf("vu-s%d", i), Seed: int64(i),
		})
	}

	fs := a.Classify("r", ClassifyConfig{AvailabilityRun: 5})
	ev := findingFor(t, fs, domain.FindingAvailability, "api-a").Evidence
	if ev == nil {
		t.Fatal("availability finding has no evidence")
	}
	if len(ev.Sessions) != 5 || ev.StatusCounts[503] != 5 {
		t.Errorf("evidence = %+v, want all 5 unavailable responses", ev)
	}
}

// TestEvidenceWithoutSessionContext: observations recorded without session
// context (e.g. the distributed streaming path when ids cannot be attributed)
// still produce the status/time distributions — only the per-session
// representatives are omitted.
func TestEvidenceWithoutSessionContext(t *testing.T) {
	base := time.Unix(1000, 0)
	a := NewAggregator()
	a.Add(RequestObservation{APIID: "api-a", StatusCode: 500, TS: base})
	a.Add(RequestObservation{APIID: "api-a", StatusCode: 500, TS: base.Add(time.Second)})

	fs := a.Classify("r", ClassifyConfig{})
	ev := findingFor(t, fs, domain.FindingContract, "api-a").Evidence
	if ev == nil {
		t.Fatal("no evidence")
	}
	if len(ev.Sessions) != 0 {
		t.Errorf("sessions = %+v, want none without session context", ev.Sessions)
	}
	if ev.StatusCounts[500] != 2 {
		t.Errorf("status counts = %+v, want {500:2}", ev.StatusCounts)
	}
}

// TestSummaryFindingsCarryNoEvidence pins the documented contract of the
// worker-aggregated path: a Summary retains no per-request data, so its
// coarse findings cannot (and do not) carry evidence bundles.
func TestSummaryFindingsCarryNoEvidence(t *testing.T) {
	s := NewSummary()
	for i := 0; i < 10; i++ {
		s.Add(RequestObservation{APIID: "api-a", StatusCode: 500, LatencyMs: 10})
	}
	for _, f := range FindingsFromSummary("r", s, DefaultClassifyConfig()) {
		if f.Evidence != nil {
			t.Errorf("summary-derived finding %v carries evidence; the summary path is documented as coarse", f.Category)
		}
	}
}
