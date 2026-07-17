package api

import (
	"context"
	"sync"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/chordpli/tmula/server/internal/safety"
)

type runState struct {
	mu        sync.Mutex
	exec      domain.RunExecution
	collector *obs.Collector
	guard     *safety.Guard
	// trace, when non-nil, buffers live per-request events for the traffic graph
	// (set only for small runs that opted in). Its methods are concurrency-safe.
	trace *traceBuf
	// heat, when non-nil, aggregates per-edge traffic for the large-scale heatmap
	// (set for any opted-in run). Its methods are concurrency-safe.
	heat *heatAgg
	// latency, when non-nil, aggregates request latencies into a time x latency
	// grid for the latency heatmap (set for any opted-in run). Concurrency-safe.
	latency  *latencyHeat
	cancel   context.CancelFunc
	done     chan struct{}
	findings []domain.Finding
	// serverMetrics / metricsErr hold the post-run Prometheus correlation for a
	// run that opted in (RunSpec.Metrics): the fetched series, and the fetch
	// problem when one occurred. Both are observability-only report extras.
	serverMetrics []domain.MetricSeries
	metricsErr    string
	// finalStats holds stats produced directly by a run (the open model returns
	// an aggregate rather than feeding the collector). When nil, the live
	// collector snapshot is used instead.
	finalStats *obs.Stats
	// bootstrap, when non-nil, is the bootstrap-signup provider+teardown for a run
	// whose credential pool provisions real accounts. It is built once in execute,
	// shared with the closed/open auth paths (so prewarm, the live auth, and
	// teardown all act on the same cached identities), and its teardown is deferred
	// for the whole run. Nil for every non-bootstrap run.
	bootstrap *bootstrapAuth
}

// stats returns the run's stats: the final aggregate if one was produced
// directly (open model), otherwise a live snapshot of the collector.
func (rs *runState) stats() obs.Stats {
	rs.mu.Lock()
	fs := rs.finalStats
	rs.mu.Unlock()
	if fs != nil {
		return *fs
	}
	return rs.collector.Snapshot()
}

func (rs *runState) snapshotStatus() (domain.RunStatus, string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.exec.Status, rs.exec.KillReason
}

// runStateTerminal reports whether a run has reached a terminal status and is
// therefore safe to evict. It briefly takes rs.mu; callers already hold s.mu, and
// no path holds rs.mu before acquiring s.mu, so the s.mu -> rs.mu order is safe.
func runStateTerminal(rs *runState) bool {
	rs.mu.Lock()
	st := rs.exec.Status
	rs.mu.Unlock()
	switch st {
	case domain.RunCompleted, domain.RunKilled, domain.RunFailed:
		return true
	default:
		return false
	}
}
