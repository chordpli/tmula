package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chordpli/tmula/internal/domain"
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
	if ms, _ := loaded.Metrics("r1"); len(ms) != 1 || ms[0].LatencyMs != 12 {
		t.Errorf("loaded metrics = %+v", ms)
	}
	if fs, _ := loaded.Findings("r1"); len(fs) != 1 {
		t.Errorf("loaded findings = %+v", fs)
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
