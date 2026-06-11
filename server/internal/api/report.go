package api

import (
	"errors"
	"log/slog"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/chordpli/tmula/server/internal/store"
)

// Report is the operator-facing run report. Run.Mode reports the execution
// topology (local or distributed); Workers is the number of remote workers a
// distributed run fanned out to (0 for a local run).
type Report struct {
	Run      domain.RunExecution `json:"run"`
	Stats    obs.Stats           `json:"stats"`
	Findings []domain.Finding    `json:"findings"`
	Workers  int                 `json:"workers"`
	// ServerMetrics carries the Prometheus series fetched over the run's window
	// when the run opted in (RunSpec.Metrics); MetricsError notes a fetch
	// problem. Both are live-report extras: they are not persisted, so a report
	// rebuilt from the store omits them.
	ServerMetrics []domain.MetricSeries `json:"serverMetrics,omitempty"`
	MetricsError  string                `json:"metricsError,omitempty"`
}

// report assembles the report for a run (caller must not hold rs.mu). Workers is
// taken from the run itself (set at creation, persisted on finalize) so the live
// report and one rebuilt from the store agree on the topology.
func (rs *runState) report() Report {
	rs.mu.Lock()
	exec := rs.exec
	findings := append([]domain.Finding(nil), rs.findings...)
	serverMetrics := append([]domain.MetricSeries(nil), rs.serverMetrics...)
	metricsErr := rs.metricsErr
	rs.mu.Unlock()
	return Report{
		Run: exec, Stats: rs.stats(), Findings: findings, Workers: exec.Workers,
		ServerMetrics: serverMetrics, MetricsError: metricsErr,
	}
}

// reportFor returns a run's report and whether it was found. A live run in the
// in-memory cache is served directly; otherwise the run is rebuilt from the
// store (the system of record), so a report stays available after the live state
// is evicted past the retention bound or lost to a restart. The bool is false
// only when neither the cache nor the store knows the run.
func (s *Server) reportFor(id domain.ID) (Report, bool) {
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if ok {
		return rs.report(), true
	}
	return s.reportFromStore(id)
}

// reportFromStore rebuilds a finalized run's report from persisted run + stats +
// findings. A missing run is the not-found case; stats or findings absent (an
// older snapshot, or a run that never finalized) degrade to zero-values rather
// than failing, since the run row alone still makes a meaningful report.
func (s *Server) reportFromStore(id domain.ID) (Report, bool) {
	if s.store == nil {
		return Report{}, false
	}
	run, err := s.store.GetRun(id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("load run from store failed", "run", id, "err", err)
		}
		return Report{}, false
	}
	stats, err := s.store.Stats(id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Warn("load stats from store failed", "run", id, "err", err)
	}
	findings, err := s.store.Findings(id)
	if err != nil {
		slog.Warn("load findings from store failed", "run", id, "err", err)
	}
	return Report{Run: run, Stats: stats, Findings: findings, Workers: run.Workers}, true
}
