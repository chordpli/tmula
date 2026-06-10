package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
)

func TestExperimentCRUD(t *testing.T) {
	s := NewMemStore()
	exp := domain.Experiment{ID: "e1", Name: "smoke"}
	if err := s.SaveExperiment(exp); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.GetExperiment("e1")
	if err != nil || got.Name != "smoke" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetExperiment("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing experiment should be ErrNotFound, got %v", err)
	}
	if err := s.SaveExperiment(domain.Experiment{}); err == nil {
		t.Error("empty id should error")
	}
}

func TestRunCRUD(t *testing.T) {
	s := NewMemStore()
	if err := s.SaveRun(domain.RunExecution{ID: "r1", Status: domain.RunRunning}); err != nil {
		t.Fatalf("save: %v", err)
	}
	r, err := s.GetRun("r1")
	if err != nil || r.Status != domain.RunRunning {
		t.Fatalf("get run = %+v, %v", r, err)
	}
	// Replace (status update).
	_ = s.SaveRun(domain.RunExecution{ID: "r1", Status: domain.RunCompleted})
	if r, _ := s.GetRun("r1"); r.Status != domain.RunCompleted {
		t.Errorf("run not updated: %s", r.Status)
	}
}

func TestStatsCRUD(t *testing.T) {
	s := NewMemStore()
	// Unknown run -> ErrNotFound, never a zero-value masquerading as real stats.
	if _, err := s.Stats("r1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing stats should be ErrNotFound, got %v", err)
	}
	if err := s.SaveStats("", obs.Stats{}); err == nil {
		t.Error("empty runId should error")
	}

	want := obs.Stats{Total: 20, Errors: 1, ErrorRate: 0.05, P95: 12.5, StatusCounts: map[int]int{200: 19, 500: 1}}
	if err := s.SaveStats("r1", want); err != nil {
		t.Fatalf("save stats: %v", err)
	}
	got, err := s.Stats("r1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if got.Total != 20 || got.Errors != 1 || got.P95 != 12.5 || got.StatusCounts[500] != 1 {
		t.Fatalf("stats = %+v, want %+v", got, want)
	}
	// Replace.
	if err := s.SaveStats("r1", obs.Stats{Total: 99}); err != nil {
		t.Fatalf("replace stats: %v", err)
	}
	if again, _ := s.Stats("r1"); again.Total != 99 {
		t.Errorf("stats not replaced: %+v", again)
	}
}

func TestMetricsAppendAndCopy(t *testing.T) {
	s := NewMemStore()
	for i := 0; i < 3; i++ {
		if err := s.AppendMetric(domain.MetricSample{RunID: "r1", StatusCode: 200 + i}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	ms, _ := s.Metrics("r1")
	if len(ms) != 3 || ms[0].StatusCode != 200 || ms[2].StatusCode != 202 {
		t.Fatalf("metrics = %+v", ms)
	}
	// Returned slice is a copy: mutating it must not affect the store.
	ms[0].StatusCode = 999
	if again, _ := s.Metrics("r1"); again[0].StatusCode != 200 {
		t.Error("Metrics should return a copy")
	}
	// Unknown run -> empty, not nil-deref.
	if got, _ := s.Metrics("none"); len(got) != 0 {
		t.Errorf("unknown run metrics = %v", got)
	}
}

func TestMetricsAppendBatch(t *testing.T) {
	s := NewMemStore()

	// Empty batch is a no-op (and must not create the run key implicitly).
	if err := s.AppendMetrics(nil); err != nil {
		t.Fatalf("AppendMetrics(nil): %v", err)
	}
	if got, _ := s.Metrics("r1"); len(got) != 0 {
		t.Fatalf("empty batch should add nothing, got %v", got)
	}

	// Batch spanning two runs lands in the right buckets, preserving order.
	batch := []domain.MetricSample{
		{RunID: "r1", StatusCode: 200},
		{RunID: "r2", StatusCode: 500},
		{RunID: "r1", StatusCode: 201},
	}
	if err := s.AppendMetrics(batch); err != nil {
		t.Fatalf("AppendMetrics: %v", err)
	}
	r1, _ := s.Metrics("r1")
	if len(r1) != 2 || r1[0].StatusCode != 200 || r1[1].StatusCode != 201 {
		t.Fatalf("r1 metrics = %+v", r1)
	}
	if r2, _ := s.Metrics("r2"); len(r2) != 1 || r2[0].StatusCode != 500 {
		t.Fatalf("r2 metrics = %+v", r2)
	}

	// A missing runId rejects the whole batch with no partial write.
	bad := []domain.MetricSample{{RunID: "r1", StatusCode: 999}, {RunID: "", StatusCode: 1}}
	if err := s.AppendMetrics(bad); err == nil {
		t.Fatal("batch with empty runId should error")
	}
	if again, _ := s.Metrics("r1"); len(again) != 2 {
		t.Fatalf("failed batch must not partially write, r1 = %+v", again)
	}

	// The caller's slice may be reused after the call: mutating it must not be
	// observed by the store (AppendMetrics must not retain the input slice).
	batch[0].StatusCode = -1
	if r1, _ := s.Metrics("r1"); r1[0].StatusCode != 200 {
		t.Errorf("store retained caller slice: r1[0] = %d", r1[0].StatusCode)
	}
}

func TestFindings(t *testing.T) {
	s := NewMemStore()
	fs := []domain.Finding{{RunID: "r1", Category: domain.FindingContract, Severity: domain.SeverityCritical}}
	if err := s.SaveFindings("r1", fs); err != nil {
		t.Fatalf("save findings: %v", err)
	}
	got, _ := s.Findings("r1")
	if len(got) != 1 || got[0].Category != domain.FindingContract {
		t.Fatalf("findings = %+v", got)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	s := NewMemStore()
	_ = s.SaveExperiment(domain.Experiment{ID: "e1", Name: "smoke"})
	_ = s.SaveRun(domain.RunExecution{ID: "r1", ExperimentID: "e1", Status: domain.RunCompleted})
	_ = s.SaveStats("r1", obs.Stats{Total: 20, Errors: 1, P95: 7.5, StatusCounts: map[int]int{200: 20}})
	_ = s.AppendMetric(domain.MetricSample{RunID: "r1", StatusCode: 200, LatencyMs: 12})
	_ = s.SaveFindings("r1", []domain.Finding{{RunID: "r1", Category: domain.FindingThreshold}})

	path := filepath.Join(t.TempDir(), "snap.json")
	if err := s.SaveToFile(path); err != nil {
		t.Fatalf("save file: %v", err)
	}

	loaded := NewMemStore()
	if err := loaded.LoadFromFile(path); err != nil {
		t.Fatalf("load file: %v", err)
	}
	if e, err := loaded.GetExperiment("e1"); err != nil || e.Name != "smoke" {
		t.Errorf("loaded experiment = %+v, %v", e, err)
	}
	if r, err := loaded.GetRun("r1"); err != nil || r.Status != domain.RunCompleted {
		t.Errorf("loaded run = %+v, %v", r, err)
	}
	if st, err := loaded.Stats("r1"); err != nil || st.Total != 20 || st.P95 != 7.5 || st.StatusCounts[200] != 20 {
		t.Errorf("loaded stats = %+v, %v", st, err)
	}
	if ms, _ := loaded.Metrics("r1"); len(ms) != 1 || ms[0].LatencyMs != 12 {
		t.Errorf("loaded metrics = %+v", ms)
	}
	if fs, _ := loaded.Findings("r1"); len(fs) != 1 {
		t.Errorf("loaded findings = %+v", fs)
	}
}

// TestReportSnapshotRoundTrip is the report-shaped round-trip the durable control
// plane relies on: a run plus the exact run + stats + findings a report is rebuilt
// from must survive a SaveToFile/LoadFromFile cycle byte-for-byte in the numbers
// the operator sees, so a report served from a reloaded snapshot matches the live
// one.
func TestReportSnapshotRoundTrip(t *testing.T) {
	end := time.Unix(1700, 0).UTC()
	run := domain.RunExecution{
		ID: "run-7", ExperimentID: "exp-1", Mode: domain.RunDistributed,
		Status: domain.RunCompleted, StartedAt: time.Unix(1600, 0).UTC(),
		EndedAt: &end, Workers: 3,
	}
	stats := obs.Stats{
		Total: 1000, Errors: 25, Timeouts: 4, ErrorRate: 0.025,
		StatusCounts: map[int]int{200: 975, 500: 25}, P50: 5, P95: 40, P99: 95, Max: 250,
	}
	findings := []domain.Finding{
		{RunID: "run-7", Category: domain.FindingAvailability, Severity: domain.SeverityCritical, Description: "saturated"},
		{RunID: "run-7", Category: domain.FindingThreshold, Severity: domain.SeverityWarning, Description: "p95 high"},
	}

	src := NewMemStore()
	if err := src.SaveRun(run); err != nil {
		t.Fatalf("save run: %v", err)
	}
	if err := src.SaveStats(run.ID, stats); err != nil {
		t.Fatalf("save stats: %v", err)
	}
	if err := src.SaveFindings(run.ID, findings); err != nil {
		t.Fatalf("save findings: %v", err)
	}

	path := filepath.Join(t.TempDir(), "report.json")
	if err := src.SaveToFile(path); err != nil {
		t.Fatalf("save file: %v", err)
	}
	dst := NewMemStore()
	if err := dst.LoadFromFile(path); err != nil {
		t.Fatalf("load file: %v", err)
	}

	gotRun, err := dst.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if gotRun.Workers != 3 || gotRun.Mode != domain.RunDistributed || gotRun.EndedAt == nil || !gotRun.EndedAt.Equal(end) {
		t.Errorf("run did not round-trip: %+v", gotRun)
	}
	gotStats, err := dst.Stats(run.ID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if gotStats.Total != 1000 || gotStats.Errors != 25 || gotStats.P99 != 95 || gotStats.StatusCounts[500] != 25 {
		t.Errorf("stats did not round-trip: %+v", gotStats)
	}
	if gotFindings, _ := dst.Findings(run.ID); len(gotFindings) != 2 || gotFindings[0].Severity != domain.SeverityCritical {
		t.Errorf("findings did not round-trip: %+v", gotFindings)
	}
}

func TestConcurrentAppend(t *testing.T) {
	s := NewMemStore()
	const goroutines, each = 16, 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_ = s.AppendMetric(domain.MetricSample{RunID: "r1", StatusCode: 200})
			}
		}()
	}
	wg.Wait()
	if ms, _ := s.Metrics("r1"); len(ms) != goroutines*each {
		t.Fatalf("metrics = %d, want %d", len(ms), goroutines*each)
	}
}
