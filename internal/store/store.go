package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/obs"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("store: not found")

// Store persists experiments, runs, metrics and findings. The local in-memory
// implementation satisfies it today; a Postgres/time-series store can satisfy
// the same interface for distributed mode without changing callers.
type Store interface {
	SaveExperiment(domain.Experiment) error
	GetExperiment(id domain.ID) (domain.Experiment, error)

	SaveRun(domain.RunExecution) error
	GetRun(id domain.ID) (domain.RunExecution, error)

	// SaveStats stores the final aggregate stats for a run. They are what a report
	// rebuilt from the store (after the live run state is evicted or the process
	// restarts) needs but cannot recompute from the run row alone, so a finalized
	// run persists them alongside its findings.
	SaveStats(runID domain.ID, stats obs.Stats) error
	// Stats returns the persisted aggregate for a run or ErrNotFound when none was
	// saved (e.g. a run that never reached a terminal state).
	Stats(runID domain.ID) (obs.Stats, error)

	AppendMetric(domain.MetricSample) error
	// AppendMetrics appends a batch of samples in one call. High-frequency runs
	// emit thousands of samples per second; batching lets a backend persist a
	// whole batch in a single round-trip instead of one insert per sample. An
	// empty batch is a no-op. Either every sample in the batch is stored or, on
	// error, none are.
	AppendMetrics(ms []domain.MetricSample) error
	Metrics(runID domain.ID) ([]domain.MetricSample, error)

	SaveFindings(runID domain.ID, findings []domain.Finding) error
	Findings(runID domain.ID) ([]domain.Finding, error)
}

// MemStore is a dependency-free, concurrency-safe in-memory Store with optional
// JSON-file snapshots (no SQLite/cgo).
type MemStore struct {
	mu          sync.RWMutex
	experiments map[domain.ID]domain.Experiment
	runs        map[domain.ID]domain.RunExecution
	stats       map[domain.ID]obs.Stats
	metrics     map[domain.ID][]domain.MetricSample
	findings    map[domain.ID][]domain.Finding
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		experiments: make(map[domain.ID]domain.Experiment),
		runs:        make(map[domain.ID]domain.RunExecution),
		stats:       make(map[domain.ID]obs.Stats),
		metrics:     make(map[domain.ID][]domain.MetricSample),
		findings:    make(map[domain.ID][]domain.Finding),
	}
}

// compile-time assertion that *MemStore implements Store.
var _ Store = (*MemStore)(nil)

// SaveExperiment stores or replaces an experiment.
func (s *MemStore) SaveExperiment(e domain.Experiment) error {
	if e.ID == "" {
		return fmt.Errorf("store: experiment id is required")
	}
	s.mu.Lock()
	s.experiments[e.ID] = e
	s.mu.Unlock()
	return nil
}

// GetExperiment returns an experiment or ErrNotFound.
func (s *MemStore) GetExperiment(id domain.ID) (domain.Experiment, error) {
	s.mu.RLock()
	e, ok := s.experiments[id]
	s.mu.RUnlock()
	if !ok {
		return domain.Experiment{}, fmt.Errorf("%w: experiment %q", ErrNotFound, id)
	}
	return e, nil
}

// SaveRun stores or replaces a run.
func (s *MemStore) SaveRun(r domain.RunExecution) error {
	if r.ID == "" {
		return fmt.Errorf("store: run id is required")
	}
	s.mu.Lock()
	s.runs[r.ID] = r
	s.mu.Unlock()
	return nil
}

// GetRun returns a run or ErrNotFound.
func (s *MemStore) GetRun(id domain.ID) (domain.RunExecution, error) {
	s.mu.RLock()
	r, ok := s.runs[id]
	s.mu.RUnlock()
	if !ok {
		return domain.RunExecution{}, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	return r, nil
}

// SaveStats stores or replaces the aggregate stats for a run.
func (s *MemStore) SaveStats(runID domain.ID, stats obs.Stats) error {
	if runID == "" {
		return fmt.Errorf("store: stats runId is required")
	}
	s.mu.Lock()
	s.stats[runID] = stats
	s.mu.Unlock()
	return nil
}

// Stats returns a run's aggregate stats or ErrNotFound.
func (s *MemStore) Stats(runID domain.ID) (obs.Stats, error) {
	s.mu.RLock()
	st, ok := s.stats[runID]
	s.mu.RUnlock()
	if !ok {
		return obs.Stats{}, fmt.Errorf("%w: stats for run %q", ErrNotFound, runID)
	}
	return st, nil
}

// AppendMetric appends one metric sample to its run.
func (s *MemStore) AppendMetric(m domain.MetricSample) error {
	if m.RunID == "" {
		return fmt.Errorf("store: metric runId is required")
	}
	s.mu.Lock()
	s.metrics[m.RunID] = append(s.metrics[m.RunID], m)
	s.mu.Unlock()
	return nil
}

// AppendMetrics appends a batch of samples under a single lock. Validation runs
// before any mutation so a bad sample rejects the whole batch without a partial
// write. An empty batch is a no-op.
func (s *MemStore) AppendMetrics(ms []domain.MetricSample) error {
	if len(ms) == 0 {
		return nil
	}
	for i := range ms {
		if ms[i].RunID == "" {
			return fmt.Errorf("store: metric[%d] runId is required", i)
		}
	}
	s.mu.Lock()
	for i := range ms {
		rid := ms[i].RunID
		s.metrics[rid] = append(s.metrics[rid], ms[i])
	}
	s.mu.Unlock()
	return nil
}

// Metrics returns a copy of the metric samples for a run (never nil).
func (s *MemStore) Metrics(runID domain.ID) ([]domain.MetricSample, error) {
	s.mu.RLock()
	src := s.metrics[runID]
	out := make([]domain.MetricSample, len(src))
	copy(out, src)
	s.mu.RUnlock()
	return out, nil
}

// SaveFindings replaces the findings for a run.
func (s *MemStore) SaveFindings(runID domain.ID, findings []domain.Finding) error {
	if runID == "" {
		return fmt.Errorf("store: findings runId is required")
	}
	cp := make([]domain.Finding, len(findings))
	copy(cp, findings)
	s.mu.Lock()
	s.findings[runID] = cp
	s.mu.Unlock()
	return nil
}

// Findings returns a copy of the findings for a run (never nil).
func (s *MemStore) Findings(runID domain.ID) ([]domain.Finding, error) {
	s.mu.RLock()
	src := s.findings[runID]
	out := make([]domain.Finding, len(src))
	copy(out, src)
	s.mu.RUnlock()
	return out, nil
}

// snapshot is the on-disk JSON shape.
type snapshot struct {
	Experiments map[domain.ID]domain.Experiment     `json:"experiments"`
	Runs        map[domain.ID]domain.RunExecution   `json:"runs"`
	Stats       map[domain.ID]obs.Stats             `json:"stats"`
	Metrics     map[domain.ID][]domain.MetricSample `json:"metrics"`
	Findings    map[domain.ID][]domain.Finding      `json:"findings"`
}

// SaveToFile writes a JSON snapshot of the store to path.
func (s *MemStore) SaveToFile(path string) error {
	s.mu.RLock()
	snap := snapshot{Experiments: s.experiments, Runs: s.runs, Stats: s.stats, Metrics: s.metrics, Findings: s.findings}
	data, err := json.MarshalIndent(snap, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("store: marshal snapshot: %w", err)
	}
	// Write to a sibling temp file then rename, so a crash or disk-full mid-write
	// cannot truncate or corrupt an existing snapshot (rename is atomic same-fs).
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("store: write snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store: replace snapshot: %w", err)
	}
	return nil
}

// LoadFromFile replaces the store's contents with a JSON snapshot from path.
func (s *MemStore) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("store: read snapshot: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("store: unmarshal snapshot: %w", err)
	}
	s.mu.Lock()
	s.experiments = nonNilExp(snap.Experiments)
	s.runs = nonNilRun(snap.Runs)
	s.stats = nonNilStats(snap.Stats)
	s.metrics = nonNilMetrics(snap.Metrics)
	s.findings = nonNilFindings(snap.Findings)
	s.mu.Unlock()
	return nil
}

func nonNilExp(m map[domain.ID]domain.Experiment) map[domain.ID]domain.Experiment {
	if m == nil {
		return make(map[domain.ID]domain.Experiment)
	}
	return m
}

func nonNilRun(m map[domain.ID]domain.RunExecution) map[domain.ID]domain.RunExecution {
	if m == nil {
		return make(map[domain.ID]domain.RunExecution)
	}
	return m
}

func nonNilStats(m map[domain.ID]obs.Stats) map[domain.ID]obs.Stats {
	if m == nil {
		return make(map[domain.ID]obs.Stats)
	}
	return m
}

func nonNilMetrics(m map[domain.ID][]domain.MetricSample) map[domain.ID][]domain.MetricSample {
	if m == nil {
		return make(map[domain.ID][]domain.MetricSample)
	}
	return m
}

func nonNilFindings(m map[domain.ID][]domain.Finding) map[domain.ID][]domain.Finding {
	if m == nil {
		return make(map[domain.ID][]domain.Finding)
	}
	return m
}
