// Package api is the control plane: a REST surface (plus an SSE progress
// stream) that ties the scenario engine, virtual-user runtime, safety guard
// and observation collector together so an operator can create, run, kill and
// report on experiments.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/chordpli/tmula/internal/auth"
	"github.com/chordpli/tmula/internal/cluster"
	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/mask"
	"github.com/chordpli/tmula/internal/obs"
	"github.com/chordpli/tmula/internal/safety"
	"github.com/chordpli/tmula/internal/store"
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
	// UserCount sizes the closed-model virtual-user pool when Users is empty: the
	// server synthesizes u0..u{UserCount-1} at run (and shard) time instead of the
	// client shipping one object per user, so a huge closed run fits in a small
	// request body rather than overflowing the create-request size limit. An
	// explicit Users list always wins; the open model ignores it (it generates its
	// own sessions from the arrival rate).
	UserCount int   `json:"userCount,omitempty"`
	Seed      int64 `json:"seed"`
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

	// CredentialPool, when set, authenticates the run: each virtual user (closed)
	// or session (open) is assigned a credential by index from the pool, so the
	// simulated traffic carries real auth material instead of running anonymously.
	// The pool strategy must be "pool" (pre-supplied entries) on this path;
	// bootstrap-signup is a documented follow-up (it needs a signup transport this
	// path does not yet wire). The credential secret carries json:"-" (domain), so
	// a persisted or streamed spec never leaks it. Nil leaves the run
	// unauthenticated, exactly as before.
	CredentialPool *domain.CredentialPool `json:"credentialPool,omitempty"`

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
	// needs no user list; every other path needs at least one user — supplied
	// either as an explicit pool or as a positive UserCount the server expands.
	if !r.isOpen() && r.poolSize() <= 0 {
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
	if err := r.validateCredentialPool(); err != nil {
		return err
	}
	return nil
}

// validateCredentialPool checks an optional credential pool is usable on this
// path. A nil pool is fine (the run is unauthenticated). The domain validation
// rejects an unknown strategy and an empty "pool" strategy; on top of that, the
// run path supports only the pre-supplied "pool" strategy today, so a
// bootstrap-signup request fails loudly rather than silently running
// unauthenticated, and a credential pool combined with distributed workers is
// refused because the worker fan-out synthesizes its own (unauthenticated) users.
func (r RunSpec) validateCredentialPool() error {
	if r.CredentialPool == nil {
		return nil
	}
	if err := r.CredentialPool.Validate(); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	if r.CredentialPool.Strategy == domain.CredBootstrapSignup {
		// Follow-up: the bootstrap provider exists but needs a signup transport this
		// run path does not yet wire. Refuse rather than run unauthenticated.
		return fmt.Errorf("api: credential strategy %q is not yet supported via this run path (follow-up); use the %q strategy with pre-supplied entries", domain.CredBootstrapSignup, domain.CredPool)
	}
	if len(r.Workers) > 0 || r.AggregateWorkers {
		return fmt.Errorf("api: a credential pool is not yet supported with distributed workers (the worker fan-out synthesizes its own users)")
	}
	return nil
}

// credentialProvider builds the auth provider for a run from its credential pool,
// or returns (nil, nil) when the run is unauthenticated. Validate has already
// confirmed the pool is a usable "pool" strategy, so no signup function is needed.
func (r RunSpec) credentialProvider() (auth.Provider, error) {
	if r.CredentialPool == nil {
		return nil, nil
	}
	return auth.NewProvider(*r.CredentialPool, nil)
}

// isOpen reports whether the spec uses the open (arrival-rate) workload model.
func (r RunSpec) isOpen() bool {
	return r.Workload != nil && r.Workload.Kind == domain.WorkloadOpen
}

// poolSize is the closed-model virtual-user count: the explicit Users length when
// the client shipped a pool, otherwise the UserCount it asked the server to
// synthesize. It sizes both the local pool and the distributed fan-out, so the two
// paths agree on how many users a count-only run drives.
func (r RunSpec) poolSize() int {
	if len(r.Users) > 0 {
		return len(r.Users)
	}
	return r.UserCount
}

// closedUsers returns the virtual-user pool for a closed run. It returns the
// explicit Users when the client sent them; otherwise it synthesizes a stable
// u0..u{UserCount-1} pool so a large run need not ship one object per user. Callers
// reach it only after Validate has ensured poolSize > 0, so the count is positive.
func (r RunSpec) closedUsers() []load.VirtualUser {
	if len(r.Users) > 0 {
		return r.Users
	}
	users := make([]load.VirtualUser, r.UserCount)
	for i := range users {
		users[i] = load.VirtualUser{ID: fmt.Sprintf("u%d", i)}
	}
	return users
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
// in-memory maps are a hot cache for live and recent runs; store is the system
// of record. When a run is absent from s.runs (evicted past the retention bound
// or gone after a restart) its report is rebuilt from store, so a finalized run
// stays reportable for as long as the store retains it.
type Server struct {
	mu      sync.Mutex
	specs   map[domain.ID]RunSpec
	runs    map[domain.ID]*runState
	shares  map[string]shareEntry
	store   store.Store
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

// WithStore sets the persistence backend (system of record) for finalized runs.
// When unset the server defaults to an in-process store.NewMemStore(), so the
// in-memory behavior and existing tests are unchanged — now backed by a store a
// report can be rebuilt from after eviction. Pass a PostgresStore for a durable,
// shared control plane. A nil store is ignored (the default is kept).
func WithStore(st store.Store) Option {
	return func(s *Server) {
		if st != nil {
			s.store = st
		}
	}
}

// NewServer builds a control-plane server using the given adapter to reach the
// system under test.
func NewServer(adapter load.Adapter, opts ...Option) *Server {
	s := &Server{
		specs:   make(map[domain.ID]RunSpec),
		runs:    make(map[domain.ID]*runState),
		shares:  make(map[string]shareEntry),
		store:   store.NewMemStore(),
		adapter: adapter,
		masker:  mask.New(mask.Config{}),
		now:     time.Now,
		mux:     http.NewServeMux(),
	}
	// Options run after the default store is set, so WithStore can replace it.
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

// maxLocalPoolUsers bounds the virtual-user pool an in-process (non-distributed)
// closed run synthesizes from UserCount. The request-body limit used to cap this
// transitively — a pool shipped as one object per user hit maxRequestBytes around
// ~270k — but now that the pool is a count, an explicit ceiling keeps a tiny
// request from asking the control plane to allocate an unbounded pool (and a
// goroutine per user). A larger closed run must fan out across workers, the path
// built for that scale.
const maxLocalPoolUsers = 1_000_000

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
	id, err := s.CreateExperiment(spec)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": string(id)})
}

// CreateExperiment validates and registers a run spec in-process, returning the
// assigned experiment id. It is the Go-level entry point the HTTP create handler
// is built on, and the path an in-process caller (the `tmula run` CLI) uses to
// submit a spec WITHOUT a JSON round-trip — which matters because a credential
// secret carries json:"-" and so would be stripped crossing the wire; keeping the
// spec in-process preserves it. The error is a bad-request-class validation error.
func (s *Server) CreateExperiment(spec RunSpec) (domain.ID, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if len(spec.Workers) == 0 && len(s.defaultWorkers) > 0 {
		spec.Workers = append([]string(nil), s.defaultWorkers...)
	}
	// A closed run with no workers executes in-process and synthesizes its whole
	// pool locally, so bound it — checked after default workers are applied, so a
	// run that will distribute (the path built for huge pools) is exempt.
	if len(spec.Workers) == 0 && !spec.isOpen() && spec.poolSize() > maxLocalPoolUsers {
		return "", fmt.Errorf("api: closed pool of %d exceeds the in-process limit of %d; distribute across workers to run larger", spec.poolSize(), maxLocalPoolUsers)
	}
	id := s.nextID("exp")
	spec.id = id
	spec.Experiment.ID = id
	s.mu.Lock()
	s.specs[id] = spec
	s.mu.Unlock()
	return id, nil
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
	runID, err := s.StartRun(id)
	if err != nil {
		var ge *guardError
		switch {
		case errors.Is(err, errExperimentNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.As(err, &ge):
			writeErr(w, http.StatusForbidden, ge.err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"runId": string(runID)})
}

// errExperimentNotFound is returned by StartRun when the experiment id is unknown,
// so the HTTP handler can map it to 404 without string-matching.
var errExperimentNotFound = errors.New("experiment not found")

// guardError wraps a safety-guard rejection (an unsafe target) so the HTTP
// handler maps it to 403 while in-process callers see the underlying cause.
type guardError struct{ err error }

func (e *guardError) Error() string { return e.err.Error() }
func (e *guardError) Unwrap() error { return e.err }

// StartRun launches the experiment identified by id and returns the new run id.
// It is the Go-level entry point the HTTP run handler is built on, and the path
// the in-process `tmula run` CLI uses so a spec carrying credential secrets never
// has to cross the wire. A missing experiment yields errExperimentNotFound; an
// unsafe target yields a *guardError; both let the handler pick the right status.
func (s *Server) StartRun(id domain.ID) (domain.ID, error) {
	s.mu.Lock()
	spec, ok := s.specs[id]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("%w: %q", errExperimentNotFound, id)
	}

	guard, err := safety.NewGuardForEnv(spec.TargetEnv, nil, false)
	if err != nil {
		return "", &guardError{err: err}
	}
	if err := guard.AllowHost(spec.TargetEnv.BaseURL); err != nil {
		return "", &guardError{err: err}
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
			Workers: len(spec.Workers),
		},
		collector: obs.NewCollector(),
		guard:     guard,
		cancel:    cancel,
		done:      make(chan struct{}),
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

	return runID, nil
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
	findings := agg.Classify(rs.exec.ID, obs.ClassifyConfig{ErrorRateThreshold: 0.2, AvailabilityRun: 5})
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

	// Persist outside rs.mu: report()/stats() take rs.mu themselves, so reading
	// them here while holding the lock would deadlock.
	s.persistRun(rs, spec)
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
	provider, err := spec.credentialProvider()
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
		Classify: obs.ClassifyConfig{ErrorRateThreshold: 0.2, AvailabilityRun: 5},
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
// mutex-guarded, so the sink is safe for concurrent use. Every observation shares
// one start-of-run timestamp, exactly as the previous slice loop assigned it, so
// findings and stats are identical.
func (s *Server) executeLocal(ctx context.Context, rs *runState, spec RunSpec, agg *obs.Aggregator) error {
	ts := s.now()
	sink := func(res load.StepResult) {
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
	users := spec.closedUsers()
	provider, err := spec.credentialProvider()
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

	shardSpec := shardSpecFor(spec)
	ts := s.now()
	// Fold each shard step into the collector + aggregator as it streams in via
	// DistributeInto, rather than receiving one ShardStep per request for the whole
	// run and looping it: bounded master memory at any request volume. The sink
	// fires concurrently from every shard's receive loop; collector.Record and
	// agg.Add are both mutex-guarded, so it is safe for concurrent use. The
	// coordinator splits the pool by count and each worker synthesizes its own shard
	// of users, so only poolSize crosses here — never a materialized user array.
	sink := func(st cluster.ShardStep) {
		rs.collector.Record(st.StatusCode, st.LatencyMs, st.ErrorClass)
		agg.Add(obs.RequestObservation{
			APIID:      domain.ID(st.APIID),
			StatusCode: st.StatusCode,
			LatencyMs:  st.LatencyMs,
			ErrorClass: st.ErrorClass,
			TS:         ts,
		})
	}
	if _, err := coord.DistributeInto(ctx, shardSpec, spec.poolSize(), sink); err != nil {
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

	summary, err := coord.DistributeSummary(ctx, shardSpecFor(spec), spec.poolSize())
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

// report assembles the report for a run (caller must not hold rs.mu). Workers is
// taken from the run itself (set at creation, persisted on finalize) so the live
// report and one rebuilt from the store agree on the topology.
func (rs *runState) report() Report {
	rs.mu.Lock()
	exec := rs.exec
	findings := append([]domain.Finding(nil), rs.findings...)
	rs.mu.Unlock()
	return Report{Run: exec, Stats: rs.stats(), Findings: findings, Workers: exec.Workers}
}

// Report returns a finalized-or-live run's report and whether it was found. It is
// the Go-level accessor the in-process CLI polls (the HTTP report handler shares
// the same lookup), so an authenticated in-process run can be observed without a
// JSON round-trip.
func (s *Server) Report(id domain.ID) (Report, bool) { return s.reportFor(id) }

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

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	rep, ok := s.reportFor(id)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, rep)
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
