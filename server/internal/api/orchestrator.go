package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/chordpli/tmula/server/internal/cluster"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/metrics"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/chordpli/tmula/server/internal/workload"
)

func (s *Server) execute(ctx context.Context, rs *runState, spec RunSpec) {
	defer close(rs.done)

	// Open model: the scheduler generates sessions over time and returns the
	// aggregate directly.
	if spec.IsOpen() {
		stats, findings, err := s.executeOpen(ctx, rs, spec)
		if err == nil {
			rs.mu.Lock()
			rs.finalStats = &stats
			rs.findings = findings
			rs.mu.Unlock()
		}
		s.finalizeRun(ctx, rs, spec, err)
		return
	}

	// Worker-aggregated distributed run: each worker summarizes its shard and the
	// master merges those into run-wide stats + findings directly (no per-request
	// stream), the same shape the open path returns.
	if len(spec.Workers) > 0 && spec.AggregateWorkers {
		stats, findings, err := s.executeDistributedSummary(ctx, rs, spec)
		if err == nil {
			rs.mu.Lock()
			rs.finalStats = &stats
			rs.findings = findings
			rs.mu.Unlock()
		}
		s.finalizeRun(ctx, rs, spec, err)
		return
	}

	// Closed model (local or distributed): feed the collector + aggregator, then
	// classify.
	agg := obs.NewAggregator()
	var err error
	if len(spec.Workers) > 0 {
		err = s.executeDistributed(ctx, rs, spec, agg)
	} else {
		err = s.executeLocal(ctx, rs, spec, agg)
	}
	// The spec's optional findings block tunes the thresholds; a run without
	// one classifies with the package defaults, exactly as before.
	findings := agg.Classify(rs.exec.ID, spec.Findings.ClassifyConfig())
	rs.mu.Lock()
	rs.findings = findings
	rs.mu.Unlock()
	s.finalizeRun(ctx, rs, spec, err)
}

// finalizeRun stamps the end time and final status of a run, then persists the
// finished run to the store so its report survives eviction from the in-memory
// cache and a process restart.
func (s *Server) finalizeRun(ctx context.Context, rs *runState, spec RunSpec, err error) {
	end := s.now()
	rs.mu.Lock()
	rs.exec.EndedAt = &end
	switch {
	case rs.exec.Status == domain.RunKilled:
		// already marked by killRun
	case ctx.Err() != nil:
		rs.exec.Status = domain.RunKilled
	case err != nil:
		rs.exec.Status = domain.RunFailed
		rs.exec.KillReason = err.Error()
	default:
		rs.exec.Status = domain.RunCompleted
	}
	rs.mu.Unlock()

	// Server-side metric correlation happens here, after the end time is
	// stamped, so the fetch window covers exactly the run. It must complete
	// before persistRun only in the trivial sense of ordering — the series are
	// live-report extras and are not persisted.
	s.fetchServerMetrics(rs, spec)

	// Persist outside rs.mu: report()/stats() take rs.mu themselves, so reading
	// them here while holding the lock would deadlock.
	s.persistRun(rs, spec)
}

// fetchServerMetrics pulls the run's opted-in Prometheus queries over the
// run's window and attaches them to the live run state. It is observability
// only and strictly fail-soft: any problem (host not allowlisted, Prometheus
// down, a bad query) becomes the report's MetricsError, never a run failure.
// It uses its own context: the run's context is often already canceled here
// (a kill or timeout), and a killed run is exactly when the operator wants the
// server-side picture.
func (s *Server) fetchServerMetrics(rs *runState, spec RunSpec) {
	if spec.Metrics == nil {
		return
	}
	// The engine reaches out only to hosts the run was allowed to touch; the
	// metrics fetch obeys the same allowlist as the simulated traffic.
	if err := rs.guard.AllowHost(spec.Metrics.PrometheusURL); err != nil {
		rs.mu.Lock()
		rs.metricsErr = err.Error()
		rs.mu.Unlock()
		return
	}

	rs.mu.Lock()
	start := rs.exec.StartedAt
	end := s.now()
	if rs.exec.EndedAt != nil {
		end = *rs.exec.EndedAt
	}
	rs.mu.Unlock()

	// 15s caps how long a slow Prometheus can hold up finalize (and with it a
	// graceful shutdown); each query is additionally bounded by the metrics
	// client's own per-request timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	series, err := metrics.Fetch(ctx, *spec.Metrics, start, end)
	rs.mu.Lock()
	rs.serverMetrics = series
	if err != nil {
		rs.metricsErr = err.Error()
		slog.Warn("server metrics fetch incomplete", "run", rs.exec.ID, "err", err)
	}
	rs.mu.Unlock()
}

// persistRun writes a finalized run's experiment, run row, aggregate stats and
// findings to the store, which is the system of record a report is rebuilt from
// once the live run state is evicted or the process restarts. It is best-effort:
// a store error is logged, not fatal, so a transient backend hiccup never crashes
// an in-flight engine — the run still serves live from memory until evicted.
func (s *Server) persistRun(rs *runState, spec RunSpec) {
	if s.store == nil {
		return
	}
	// A persist failure is logged at ERROR, not WARN: on a durable (master/Postgres)
	// backend it means this run's report is lost the moment it leaves the cache, so
	// it must be alertable. It stays non-fatal — a transient backend hiccup must not
	// crash an in-flight engine — and the in-process MemStore (the local default)
	// does not fail mid-run, so this never fires false alarms there.
	rep := rs.report()
	if err := s.store.SaveExperiment(spec.Experiment); err != nil {
		slog.Error("persist experiment failed", "run", rep.Run.ID, "err", err)
	}
	if err := s.store.SaveRun(rep.Run); err != nil {
		slog.Error("persist run failed", "run", rep.Run.ID, "err", err)
	}
	if err := s.store.SaveStats(rep.Run.ID, rep.Stats); err != nil {
		slog.Error("persist stats failed", "run", rep.Run.ID, "err", err)
	}
	if err := s.store.SaveFindings(rep.Run.ID, rep.Findings); err != nil {
		slog.Error("persist findings failed", "run", rep.Run.ID, "err", err)
	}
}

// executeOpen runs the experiment with the open (arrival-rate) workload model:
// the scheduler generates user sessions over time and returns the aggregate
// stats + findings directly.
func (s *Server) executeOpen(ctx context.Context, rs *runState, spec RunSpec) (obs.Stats, []domain.Finding, error) {
	runner := s.runnerFor(rs, spec)
	user := load.VirtualUser{ID: "user"}
	if len(spec.Users) > 0 {
		user = spec.Users[0]
	}
	// When a credential pool is set, the scheduler assigns each arrival a
	// credential by its global session index; nil leaves sessions unauthenticated.
	provider, err := spec.CredentialProvider()
	if err != nil {
		return obs.Stats{}, nil, err
	}
	res, err := workload.New(runner).Run(ctx, workload.Options{
		Graph:    spec.Graph,
		Start:    spec.Start,
		MaxSteps: spec.MaxSteps,
		Model:    *spec.Workload,
		User:     user,
		Seed:     spec.Seed,
		RunID:    rs.exec.ID,
		// The spec's optional findings block tunes the classifier thresholds
		// (defaults when nil), identical to the closed paths.
		Classify: spec.Findings.ClassifyConfig(),
		// Feed the run's own collector so the SSE stream reports live progress
		// while the open run is still generating traffic, not just at the end.
		Collector: rs.collector,
		// Persona mix: drives a weighted blend of entry points and pacing.
		Segments: spec.Segments,
		// Auth, when non-nil, makes each session authenticate as a distinct
		// principal keyed by its arrival index (a pool wraps around its entries).
		Auth: provider,
	})
	if err != nil {
		return obs.Stats{}, nil, err
	}
	return res.Stats, res.Findings, nil
}

// runnerFor builds the load.Runner for a run: always guarded by the run's
// safety policy, and wired to stream live per-request events when the run opted
// into tracing. Extra options (e.g. a WithResultSink that folds each result into
// the collector incrementally) are appended last so callers can layer on behavior
// without duplicating the guard/event-sink wiring.
func (s *Server) runnerFor(rs *runState, spec RunSpec, extra ...load.RunnerOption) *load.Runner {
	opts := []load.RunnerOption{
		load.WithGuard(rs.guard),
		load.WithCorrelationIDs(rs.exec.ID, scenarioIDForSpec(spec)),
		// The experiment's deviation rate flows into every session's walk (both
		// the closed Run fan-out and the open scheduler's RunSession); 0 — the
		// default — leaves the weighted happy path untouched.
		load.WithDeviation(spec.Experiment.Params.DeviationRate),
	}
	if spec.Workload != nil {
		// Closed-model think time: Run paces each user from the workload model's
		// range. The open path is unaffected — its scheduler passes think to
		// RunSession explicitly, and the runner-level value applies only to Run.
		opts = append(opts, load.WithThinkTime(spec.Workload.ThinkTime))
	}
	if rs.heat != nil || rs.trace != nil || rs.latency != nil {
		heat, trace, latency := rs.heat, rs.trace, rs.latency
		opts = append(opts, load.WithEventSink(func(e load.StepEvent) {
			if heat != nil {
				heat.record(e)
			}
			if trace != nil {
				trace.add(e)
			}
			// Terminal events mark reaching a no-request node (done/exit); they
			// carry no latency, so keep them out of the latency grid.
			if latency != nil && !e.Terminal {
				latency.record(e.LatencyMs, time.Now())
			}
		}))
	}
	opts = append(opts, extra...)
	return load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, spec.Templates, opts...)
}

// executeLocal runs the experiment in-process via the load.Runner, folding each
// step into the run's collector and finding aggregator as it completes rather
// than materializing the whole run first. The result sink fires from many session
// goroutines at once; both the collector's Record and the aggregator's Add are
// mutex-guarded, so the sink is safe for concurrent use. Each observation is
// stamped with s.now() at the moment its result is recorded (its completion time),
// matching the latency heatmap beside it, so the timestamp-ordered availability
// classifier reflects real per-request timing; stats are unaffected (they ignore TS).
func (s *Server) executeLocal(ctx context.Context, rs *runState, spec RunSpec, agg *obs.Aggregator) error {
	sink := func(res load.StepResult) {
		cls := errorClass(res)
		rs.collector.Record(res.Resp.StatusCode, res.Resp.LatencyMs, cls)
		agg.Add(obs.RequestObservation{
			APIID:      res.NodeID,
			StatusCode: res.Resp.StatusCode,
			LatencyMs:  res.Resp.LatencyMs,
			ErrorClass: cls,
			TS:         s.now(),
		})
	}
	// Wiring the sink at construction makes the Runner stream each result into the
	// collector + aggregator and return nothing, so a huge in-process run never
	// buffers its results. closedUsers synthesizes the pool from UserCount when the
	// client sent only a count (the large-run path), or returns the explicit pool.
	users, err := s.authenticateClosedUsers(ctx, spec)
	if err != nil {
		return err
	}
	runner := s.runnerFor(rs, spec, load.WithResultSink(sink))
	_, err = runner.Run(ctx, spec.Graph, spec.Start, spec.MaxSteps, users, spec.Seed)
	return err
}

// authenticateClosedUsers materializes the closed-model pool and, when the spec
// carries a credential pool, assigns each user the credential keyed by its index
// so user i always authenticates as Acquire(i) (a pool wraps around its entries).
// With no credential pool it returns the pool unchanged (every user
// unauthenticated), so a run without auth is byte-for-byte what it was before.
// The pool provider's Acquire is pure, so the per-user assignment is deterministic
// and independent of the seeded traversal.
func (s *Server) authenticateClosedUsers(ctx context.Context, spec RunSpec) ([]load.VirtualUser, error) {
	users := spec.ClosedUsers()
	provider, err := spec.CredentialProvider()
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return users, nil
	}
	for i := range users {
		cred, err := provider.Acquire(ctx, i)
		if err != nil {
			return nil, fmt.Errorf("api: acquire credential for user %d: %w", i, err)
		}
		users[i].Cred = cred
	}
	return users, nil
}

// executeDistributed dials each worker, fans the run's virtual users out across
// them via a cluster.Coordinator, and feeds the returned per-step results into
// the same collector + finding aggregator the local path uses, so findings are
// produced identically regardless of topology.
func (s *Server) executeDistributed(ctx context.Context, rs *runState, spec RunSpec, agg *obs.Aggregator) error {
	conns, closeAll, err := dialWorkers(spec.Workers)
	if err != nil {
		return err
	}
	defer closeAll()

	coord, err := cluster.NewCoordinator(conns...)
	if err != nil {
		return fmt.Errorf("api: build coordinator: %w", err)
	}

	shardSpec := shardSpecFor(spec, rs.exec.ID)
	// Fold each shard step into the collector + aggregator as it streams in via
	// DistributeInto, rather than receiving one ShardStep per request for the whole
	// run and looping it: bounded master memory at any request volume. The sink
	// fires concurrently from every shard's receive loop; collector.Record and
	// agg.Add are both mutex-guarded, so it is safe for concurrent use. The master
	// stamps each streamed worker result with s.now() at receive time, so the
	// timestamp-ordered availability classifier reflects per-request timing; stats
	// ignore TS and are unaffected. The coordinator splits the pool by count and each
	// worker synthesizes its own shard of users, so only poolSize crosses here —
	// never a materialized user array.
	sink := func(st cluster.ShardStep) {
		rs.collector.Record(st.StatusCode, st.LatencyMs, st.ErrorClass)
		agg.Add(obs.RequestObservation{
			APIID:      domain.ID(st.APIID),
			StatusCode: st.StatusCode,
			LatencyMs:  st.LatencyMs,
			ErrorClass: st.ErrorClass,
			TS:         s.now(),
		})
	}
	if _, err := coord.DistributeInto(ctx, shardSpec, spec.PoolSize(), sink); err != nil {
		return fmt.Errorf("api: distribute run: %w", err)
	}
	return nil
}

// executeDistributedSummary runs a distributed experiment with worker-side
// aggregation: each worker folds its whole shard into one summary and the master
// merges them, so no per-request results cross the network. It returns run-wide
// stats and findings derived from the merged summary — coarser than the
// streaming path (run-wide, not per-endpoint, no run-length availability), which
// is the documented trade for bounded cost at huge volumes.
func (s *Server) executeDistributedSummary(ctx context.Context, rs *runState, spec RunSpec) (obs.Stats, []domain.Finding, error) {
	conns, closeAll, err := dialWorkers(spec.Workers)
	if err != nil {
		return obs.Stats{}, nil, err
	}
	defer closeAll()

	coord, err := cluster.NewCoordinator(conns...)
	if err != nil {
		return obs.Stats{}, nil, fmt.Errorf("api: build coordinator: %w", err)
	}

	summary, err := coord.DistributeSummary(ctx, shardSpecFor(spec, rs.exec.ID), spec.PoolSize())
	if err != nil {
		return obs.Stats{}, nil, fmt.Errorf("api: distribute summary: %w", err)
	}
	// Classification happens master-side on the merged summary, so the spec's
	// findings block (defaults when nil) applies here without traveling to the
	// workers.
	cfg := spec.Findings.ClassifyConfig()
	return summary.Stats(), obs.FindingsFromSummary(rs.exec.ID, summary, cfg), nil
}

// dialWorkers opens an insecure gRPC client connection to each worker address
// and returns the connections plus a single closer that shuts them all down. On
// any dial failure it closes whatever it already opened and returns the error,
// so callers never leak a half-open set.
func dialWorkers(addrs []string) ([]grpc.ClientConnInterface, func(), error) {
	conns := make([]grpc.ClientConnInterface, 0, len(addrs))
	closers := make([]func() error, 0, len(addrs))
	closeAll := func() {
		for _, c := range closers {
			_ = c()
		}
	}
	for _, addr := range addrs {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("api: dial worker %q: %w", addr, err)
		}
		conns = append(conns, conn)
		closers = append(closers, conn.Close)
	}
	return conns, closeAll, nil
}

// shardSpecFor maps a control-plane RunSpec onto the cluster.ShardSpec shipped
// to each worker. The per-worker user partition is computed by the Coordinator,
// so only the run-wide fields cross here.
func shardSpecFor(spec RunSpec, runID domain.ID) cluster.ShardSpec {
	// Closed-model think time travels with the shard when a workload model was
	// supplied (a distributed spec is never open — Validate refuses that), so a
	// worker paces its users exactly like a local run.
	var think domain.ThinkTime
	if spec.Workload != nil {
		think = spec.Workload.ThinkTime
	}
	return cluster.ShardSpec{
		RunID:         runID,
		ScenarioID:    scenarioIDForSpec(spec),
		Graph:         spec.Graph,
		Templates:     spec.Templates,
		TargetBaseURL: spec.TargetEnv.BaseURL,
		Start:         spec.Start,
		MaxSteps:      spec.MaxSteps,
		Seed:          spec.Seed,
		// Ship the experiment's deviation rate so each worker's sessions deviate
		// exactly as a local run would.
		DeviationRate: spec.Experiment.Params.DeviationRate,
		ThinkTime:     think,
		// Ship the safety policy so each worker enforces the same allowlist and
		// rate/concurrency cap on the target it was handed.
		Allowlist: spec.TargetEnv.Allowlist,
		RateCap:   spec.TargetEnv.RateCap,
		EnvClass:  spec.TargetEnv.EnvClass,
	}
}

func scenarioIDForSpec(spec RunSpec) domain.ID {
	if spec.Graph.ID != "" {
		return spec.Graph.ID
	}
	return spec.Experiment.ScenarioGraphID
}

func errorClass(res load.StepResult) string {
	if res.Err != nil {
		return "transport"
	}
	return ""
}
