// Package api is the control plane: a REST surface (plus an SSE progress
// stream) that ties the scenario engine, virtual-user runtime, safety guard
// and observation collector together so an operator can create, run, kill and
// report on experiments.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/chordpli/tmula/internal/cluster"
	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/mask"
	"github.com/chordpli/tmula/internal/obs"
	"github.com/chordpli/tmula/internal/safety"
	"github.com/chordpli/tmula/internal/workload"
)

// RunSpec is a self-contained experiment definition: everything needed to run.
type RunSpec struct {
	Experiment domain.Experiment                `json:"experiment"`
	TargetEnv  domain.TargetEnv                 `json:"targetEnv"`
	Graph      domain.ScenarioGraph             `json:"graph"`
	Templates  map[domain.ID]domain.APITemplate `json:"templates"`
	Start      domain.ID                        `json:"start"`
	MaxSteps   int                              `json:"maxSteps"`
	Users      []load.VirtualUser               `json:"users"`
	Seed       int64                            `json:"seed"`
	// Workers lists gRPC worker addresses to distribute the run across. When
	// empty the run executes locally in-process; when set, the control plane
	// dials each worker, fans the virtual users out across them, and aggregates
	// their streamed results identically to the local path.
	Workers []string `json:"workers,omitempty"`

	// AggregateWorkers makes a distributed run aggregate on the workers: each
	// worker folds its whole shard into a compact summary and the master merges
	// those, instead of streaming every request. It trades per-endpoint and
	// run-length finding fidelity for bounded network + memory at huge request
	// volumes. Ignored unless Workers is set.
	AggregateWorkers bool `json:"aggregateWorkers,omitempty"`

	// Workload selects the user-generation model. Nil or a closed model runs a
	// fixed set of users (the default); an open model generates sessions at an
	// arrival rate over time so concurrency emerges organically.
	Workload *domain.WorkloadModel `json:"workload,omitempty"`

	// Segments is the persona mix for an open run: weighted behavioral profiles
	// (entry node, step bound, think time) the arrivals are drawn from. It only
	// applies to the open model; the closed path ignores it.
	Segments []domain.Segment `json:"segments,omitempty"`

	// Trace opts a small run (<= traceMaxUsers) into live per-request event
	// streaming for the traffic graph (GET /runs/{id}/trace). Larger runs ignore
	// it — it is an inspect view, not a millions-scale feature.
	Trace bool `json:"trace,omitempty"`

	id domain.ID
}

// Validate checks the spec is runnable.
func (r RunSpec) Validate() error {
	if err := r.TargetEnv.Validate(); err != nil {
		return err
	}
	if err := r.Graph.Validate(); err != nil {
		return err
	}
	if err := r.Experiment.Validate(); err != nil {
		return err
	}
	// Validate every template's path so a static authority/scheme/CRLF cannot be
	// smuggled into the request URL. (A variable that renders into the path is
	// additionally caught at request time by the guard's allowlist check.)
	for id, t := range r.Templates {
		if t.Method == "" {
			return fmt.Errorf("api: template %q: method is required", id)
		}
		if err := validateTemplatePath(t.Path); err != nil {
			return fmt.Errorf("api: template %q path %q: %w", id, t.Path, err)
		}
	}
	if r.Start == "" {
		return fmt.Errorf("api: start node is required")
	}
	if r.Workload != nil {
		if err := r.Workload.Validate(); err != nil {
			return err
		}
	}
	// The open model generates its own sessions from the arrival rate, so it
	// needs no user list; every other path needs at least one user.
	if !r.isOpen() && len(r.Users) == 0 {
		return fmt.Errorf("api: at least one virtual user is required")
	}
	// The open model runs in-process only; refuse worker fields rather than
	// silently dropping them and running locally.
	if r.isOpen() && (len(r.Workers) > 0 || r.AggregateWorkers) {
		return fmt.Errorf("api: distributed workers are not supported with the open workload model")
	}
	if len(r.Segments) > 0 {
		if !r.isOpen() {
			return fmt.Errorf("api: segments (personas) apply only to the open workload model")
		}
		if err := domain.ValidateSegments(r.Segments); err != nil {
			return err
		}
		// A segment's entry node must exist in the graph, else its sessions would
		// fail to walk at runtime; reject up front with a clear message.
		nodes := make(map[domain.ID]bool, len(r.Graph.Nodes))
		for _, n := range r.Graph.Nodes {
			nodes[n.ID] = true
		}
		for _, seg := range r.Segments {
			if seg.Start != "" && !nodes[seg.Start] {
				return fmt.Errorf("api: segment %q start node %q is not in the graph", seg.Name, seg.Start)
			}
		}
	}
	return nil
}

// isOpen reports whether the spec uses the open (arrival-rate) workload model.
func (r RunSpec) isOpen() bool {
	return r.Workload != nil && r.Workload.Kind == domain.WorkloadOpen
}

// validateTemplatePath rejects a template path that could redirect a request off
// the target host: it must be a rooted path (start with a single "/"), carry no
// scheme or authority, and contain no control characters. A "//" prefix is
// refused because it is a protocol-relative authority.
func validateTemplatePath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("must be a rooted path starting with /")
	}
	if strings.HasPrefix(path, "//") {
		return fmt.Errorf("must not start with // (protocol-relative authority)")
	}
	if strings.Contains(path, "://") {
		return fmt.Errorf("must not contain a scheme")
	}
	if strings.ContainsAny(path, "\r\n\t") {
		return fmt.Errorf("must not contain control characters")
	}
	return nil
}

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
	// finalStats holds stats produced directly by a run (the open model returns
	// an aggregate rather than feeding the collector). When nil, the live
	// collector snapshot is used instead.
	finalStats *obs.Stats
	// workers is the number of remote workers a distributed run fanned out to
	// (0 for a local run). It is fixed at run creation, so it needs no locking.
	workers int
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

// Server holds the in-memory registries and serves the control plane. The
// store is intentionally in-memory; the persistent store (#14) plugs in later.
type Server struct {
	mu      sync.Mutex
	specs   map[domain.ID]RunSpec
	runs    map[domain.ID]*runState
	shares  map[string]shareEntry
	adapter load.Adapter
	masker  *mask.Masker
	// runOrder records run IDs in insertion order so the retention bound can evict
	// the oldest terminal runs first. shareOrder does the same for share tokens.
	runOrder       []domain.ID
	shareOrder     []string
	defaultWorkers []string
	// importFn, when set (WithImporter), converts an uploaded OpenAPI/HAR spec into
	// a RunSpec for POST /import. Injected so the api package avoids the
	// importer/scenariofile import cycle (both depend on api).
	importFn ImportFunc
	seq      atomic.Int64
	now      func() time.Time
	mux      *http.ServeMux
}

// maxRetainedRuns and maxRetainedShares bound the in-memory registries so a
// long-lived control plane cannot grow without limit. When exceeded the oldest
// TERMINAL runs (and their specs) are evicted; a running or pending run is never
// evicted, so the live set can briefly exceed the cap if every old run is still
// in flight. Shares are capped the same way, oldest-first.
const (
	maxRetainedRuns   = 1000
	maxRetainedShares = 1000
)

// Option customizes a Server at construction.
type Option func(*Server)

// WithDefaultWorkers sets gRPC worker addresses applied to any experiment whose
// own spec does not list workers. It is an operator convenience so a master can
// be launched pointing at a fixed worker pool; per-experiment Workers always win.
func WithDefaultWorkers(addrs []string) Option {
	return func(s *Server) { s.defaultWorkers = addrs }
}

// NewServer builds a control-plane server using the given adapter to reach the
// system under test.
func NewServer(adapter load.Adapter, opts ...Option) *Server {
	s := &Server{
		specs:   make(map[domain.ID]RunSpec),
		runs:    make(map[domain.ID]*runState),
		shares:  make(map[string]shareEntry),
		adapter: adapter,
		masker:  mask.New(mask.Config{}),
		now:     time.Now,
		mux:     http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.routes()
	return s
}

// Handler exposes the control-plane routes.
func (s *Server) Handler() http.Handler { return s.mux }

// maxRequestBytes bounds decoded request bodies so a huge/streaming POST cannot
// exhaust memory.
const maxRequestBytes = 4 << 20 // 4 MiB

// Shutdown cancels every in-flight run and waits for their goroutines to drain,
// or until ctx is done. Call it during graceful shutdown so background runs are
// not abandoned.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	dones := make([]<-chan struct{}, 0, len(s.runs))
	for _, rs := range s.runs {
		rs.cancel()
		dones = append(dones, rs.done)
	}
	s.mu.Unlock()
	for _, d := range dones {
		select {
		case <-d:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /experiments", s.createExperiment)
	s.mux.HandleFunc("GET /experiments/{id}", s.getExperiment)
	s.mux.HandleFunc("POST /experiments/{id}/run", s.runExperiment)
	s.mux.HandleFunc("POST /runs/{id}/kill", s.killRun)
	s.mux.HandleFunc("GET /runs/{id}/report", s.getReport)
	s.mux.HandleFunc("GET /runs/{id}/report.html", s.getReportHTML)
	s.mux.HandleFunc("GET /runs/compare", s.compareRuns)
	s.mux.HandleFunc("GET /runs/{id}/stream", s.streamRun)
	s.mux.HandleFunc("GET /runs/{id}/trace", s.streamTrace)
	s.mux.HandleFunc("GET /runs/{id}/heatmap", s.streamHeatmap)
	s.mux.HandleFunc("GET /runs/{id}/latency-heatmap", s.streamLatencyHeatmap)
	s.mux.HandleFunc("POST /runs/{id}/share", s.createShare)
	s.mux.HandleFunc("GET /reports/shared/{token}", s.getSharedReport)
	s.mux.HandleFunc("GET /capacity", s.getCapacity)
	s.mux.HandleFunc("POST /import", s.handleImport)
}

// getCapacity estimates what a target population implies for a run via Little's
// Law: GET /capacity?totalUsers=&windowSeconds=&avgSessionSeconds=&perWorkerCap=
func (s *Server) getCapacity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	totalUsers := atoiDefault(q.Get("totalUsers"), 0)
	windowSeconds := atofDefault(q.Get("windowSeconds"), 0)
	avgSessionSeconds := atofDefault(q.Get("avgSessionSeconds"), 0)
	perWorkerCap := atoiDefault(q.Get("perWorkerCap"), 2000)
	if totalUsers <= 0 || windowSeconds <= 0 || avgSessionSeconds <= 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("totalUsers, windowSeconds and avgSessionSeconds must be > 0"))
		return
	}
	writeJSON(w, http.StatusOK, domain.PlanCapacity(totalUsers, windowSeconds, avgSessionSeconds, perWorkerCap))
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func atofDefault(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return def
}

func (s *Server) nextID(prefix string) domain.ID {
	return domain.ID(fmt.Sprintf("%s-%d", prefix, s.seq.Add(1)))
}

// registerRunLocked records a run in the registry and its insertion order. The
// caller must hold s.mu.
func (s *Server) registerRunLocked(id domain.ID, rs *runState) {
	s.runs[id] = rs
	s.runOrder = append(s.runOrder, id)
}

// enforceRunCapLocked evicts the oldest TERMINAL runs (and their specs) until the
// retained-run count is at or below cap. A running or pending run is skipped and
// never evicted, so when the oldest runs are all still in flight the set may stay
// above cap until they finish. The caller must hold s.mu.
func (s *Server) enforceRunCapLocked(cap int) {
	if cap <= 0 || len(s.runs) <= cap {
		return
	}
	kept := s.runOrder[:0:0] // fresh backing array; we rewrite the order slice
	for _, id := range s.runOrder {
		rs, ok := s.runs[id]
		if !ok {
			continue // already gone: drop the stale order entry
		}
		// Evict the oldest terminal runs first, but only while still over cap.
		// len(s.runs) shrinks with each delete, so the guard tracks the live count.
		if len(s.runs) > cap && runStateTerminal(rs) {
			delete(s.runs, id)
			delete(s.specs, id)
			continue
		}
		kept = append(kept, id)
	}
	s.runOrder = kept
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

func (s *Server) createExperiment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var spec RunSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode: %w", err))
		return
	}
	if err := spec.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(spec.Workers) == 0 && len(s.defaultWorkers) > 0 {
		spec.Workers = append([]string(nil), s.defaultWorkers...)
	}
	id := s.nextID("exp")
	spec.id = id
	spec.Experiment.ID = id
	s.mu.Lock()
	s.specs[id] = spec
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"id": string(id)})
}

func (s *Server) getExperiment(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	s.mu.Lock()
	spec, ok := s.specs[id]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("experiment %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

func (s *Server) runExperiment(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	s.mu.Lock()
	spec, ok := s.specs[id]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("experiment %q not found", id))
		return
	}

	guard, err := safety.NewGuardForEnv(spec.TargetEnv, nil, false)
	if err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	if err := guard.AllowHost(spec.TargetEnv.BaseURL); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}

	mode := domain.RunLocal
	if len(spec.Workers) > 0 {
		mode = domain.RunDistributed
	}

	runID := s.nextID("run")
	ctx, cancel := context.WithCancel(context.Background())
	rs := &runState{
		exec: domain.RunExecution{
			ID: runID, ExperimentID: id, Mode: mode,
			Status: domain.RunRunning, StartedAt: s.now(),
		},
		collector: obs.NewCollector(),
		guard:     guard,
		cancel:    cancel,
		done:      make(chan struct{}),
		workers:   len(spec.Workers),
	}
	// Visualization opt-in: aggregate per-edge traffic (any scale) for the
	// heatmap, and additionally buffer per-request events for the live-dot graph
	// when the run is small enough.
	if spec.Trace {
		rs.heat = newHeatAgg(spec.Graph)
		// The latency grid measures time from the run's real start, independent of
		// the server's injected clock, so it buckets against the same wall clock the
		// event sink stamps each request with.
		rs.latency = newLatencyHeat(time.Now())
		if traceSmallEnough(spec) {
			rs.trace = newTraceBuf()
		}
	}
	s.mu.Lock()
	s.registerRunLocked(runID, rs)
	s.enforceRunCapLocked(maxRetainedRuns)
	s.mu.Unlock()

	go s.execute(ctx, rs, spec)

	writeJSON(w, http.StatusAccepted, map[string]string{"runId": string(runID)})
}

func (s *Server) execute(ctx context.Context, rs *runState, spec RunSpec) {
	defer close(rs.done)

	// Open model: the scheduler generates sessions over time and returns the
	// aggregate directly.
	if spec.isOpen() {
		stats, findings, err := s.executeOpen(ctx, rs, spec)
		if err == nil {
			rs.mu.Lock()
			rs.finalStats = &stats
			rs.findings = findings
			rs.mu.Unlock()
		}
		s.finalizeRun(ctx, rs, err)
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
		s.finalizeRun(ctx, rs, err)
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
	findings := agg.Classify(rs.exec.ID, obs.ClassifyConfig{ErrorRateThreshold: 0.2, AvailabilityRun: 5})
	rs.mu.Lock()
	rs.findings = findings
	rs.mu.Unlock()
	s.finalizeRun(ctx, rs, err)
}

// finalizeRun stamps the end time and final status of a run.
func (s *Server) finalizeRun(ctx context.Context, rs *runState, err error) {
	end := s.now()
	rs.mu.Lock()
	defer rs.mu.Unlock()
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
	res, err := workload.New(runner).Run(ctx, workload.Options{
		Graph:    spec.Graph,
		Start:    spec.Start,
		MaxSteps: spec.MaxSteps,
		Model:    *spec.Workload,
		User:     user,
		Seed:     spec.Seed,
		RunID:    rs.exec.ID,
		Classify: obs.ClassifyConfig{ErrorRateThreshold: 0.2, AvailabilityRun: 5},
		// Feed the run's own collector so the SSE stream reports live progress
		// while the open run is still generating traffic, not just at the end.
		Collector: rs.collector,
		// Persona mix: drives a weighted blend of entry points and pacing.
		Segments: spec.Segments,
	})
	if err != nil {
		return obs.Stats{}, nil, err
	}
	return res.Stats, res.Findings, nil
}

// runnerFor builds the load.Runner for a run: always guarded by the run's
// safety policy, and wired to stream live per-request events when the run opted
// into tracing.
func (s *Server) runnerFor(rs *runState, spec RunSpec) *load.Runner {
	opts := []load.RunnerOption{load.WithGuard(rs.guard)}
	if rs.heat != nil || rs.trace != nil || rs.latency != nil {
		heat, trace, latency := rs.heat, rs.trace, rs.latency
		opts = append(opts, load.WithEventSink(func(e load.StepEvent) {
			if heat != nil {
				heat.record(e)
			}
			if trace != nil {
				trace.add(e)
			}
			if latency != nil {
				latency.record(e.LatencyMs, time.Now())
			}
		}))
	}
	return load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, spec.Templates, opts...)
}

// executeLocal runs the experiment in-process via the load.Runner, recording
// each step into the run's collector and finding aggregator.
func (s *Server) executeLocal(ctx context.Context, rs *runState, spec RunSpec, agg *obs.Aggregator) error {
	runner := s.runnerFor(rs, spec)
	results, err := runner.Run(ctx, spec.Graph, spec.Start, spec.MaxSteps, spec.Users, spec.Seed)
	ts := s.now()
	for _, res := range results {
		cls := errorClass(res)
		rs.collector.Record(res.Resp.StatusCode, res.Resp.LatencyMs, cls)
		agg.Add(obs.RequestObservation{
			APIID:      res.NodeID,
			StatusCode: res.Resp.StatusCode,
			LatencyMs:  res.Resp.LatencyMs,
			ErrorClass: cls,
			TS:         ts,
		})
	}
	return err
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

	shardSpec := shardSpecFor(spec)
	ts := s.now()
	_, steps, err := coord.Distribute(ctx, shardSpec, len(spec.Users))
	if err != nil {
		return fmt.Errorf("api: distribute run: %w", err)
	}
	for _, st := range steps {
		rs.collector.Record(st.StatusCode, st.LatencyMs, st.ErrorClass)
		agg.Add(obs.RequestObservation{
			APIID:      domain.ID(st.APIID),
			StatusCode: st.StatusCode,
			LatencyMs:  st.LatencyMs,
			ErrorClass: st.ErrorClass,
			TS:         ts,
		})
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

	summary, err := coord.DistributeSummary(ctx, shardSpecFor(spec), len(spec.Users))
	if err != nil {
		return obs.Stats{}, nil, fmt.Errorf("api: distribute summary: %w", err)
	}
	cfg := obs.ClassifyConfig{ErrorRateThreshold: 0.2, AvailabilityRun: 5}
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
func shardSpecFor(spec RunSpec) cluster.ShardSpec {
	return cluster.ShardSpec{
		Graph:         spec.Graph,
		Templates:     spec.Templates,
		TargetBaseURL: spec.TargetEnv.BaseURL,
		Start:         spec.Start,
		MaxSteps:      spec.MaxSteps,
		Seed:          spec.Seed,
		// Ship the safety policy so each worker enforces the same allowlist and
		// rate/concurrency cap on the target it was handed.
		Allowlist: spec.TargetEnv.Allowlist,
		RateCap:   spec.TargetEnv.RateCap,
		EnvClass:  spec.TargetEnv.EnvClass,
	}
}

func errorClass(res load.StepResult) string {
	if res.Err != nil {
		return "transport"
	}
	return ""
}

func (s *Server) killRun(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", id))
		return
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "operator kill"
	}
	rs.mu.Lock()
	if rs.exec.Status != domain.RunRunning {
		st := rs.exec.Status
		rs.mu.Unlock()
		writeErr(w, http.StatusConflict, fmt.Errorf("run %q is not running (status: %s)", id, st))
		return
	}
	rs.exec.Status = domain.RunKilled
	rs.exec.KillReason = reason
	rs.mu.Unlock()
	rs.guard.Kill(reason)
	rs.cancel()
	writeJSON(w, http.StatusOK, map[string]string{"status": "killing"})
}

// Report is the operator-facing run report. Run.Mode reports the execution
// topology (local or distributed); Workers is the number of remote workers a
// distributed run fanned out to (0 for a local run).
type Report struct {
	Run      domain.RunExecution `json:"run"`
	Stats    obs.Stats           `json:"stats"`
	Findings []domain.Finding    `json:"findings"`
	Workers  int                 `json:"workers"`
}

// report assembles the report for a run (caller must not hold rs.mu).
func (rs *runState) report() Report {
	rs.mu.Lock()
	exec := rs.exec
	findings := append([]domain.Finding(nil), rs.findings...)
	workers := rs.workers
	rs.mu.Unlock()
	return Report{Run: exec, Stats: rs.stats(), Findings: findings, Workers: workers}
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, rs.report())
}

func (s *Server) streamRun(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", id))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	emit := func() {
		status, reason := rs.snapshotStatus()
		ev := map[string]any{"status": status, "reason": reason, "stats": rs.stats()}
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-rs.done:
			emit() // final frame
			return
		case <-ticker.C:
			emit()
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
