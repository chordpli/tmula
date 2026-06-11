// Package api is the control plane: a REST surface (plus an SSE progress
// stream) that ties the scenario engine, virtual-user runtime, safety guard
// and observation collector together so an operator can create, run, kill and
// report on experiments.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/mask"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/chordpli/tmula/server/internal/runspec"
	"github.com/chordpli/tmula/server/internal/safety"
	"github.com/chordpli/tmula/server/internal/store"
)

// RunSpec re-exports runspec.RunSpec so the control plane (and cmd) can keep
// naming the type as api.RunSpec; the definition lives in the leaf runspec
// package so config producers can use it without importing api.
type RunSpec = runspec.RunSpec

// Server holds the in-memory registries and serves the control plane. The
// in-memory maps are a hot cache for live and recent runs; store is the system
// of record. When a run is absent from s.runs (evicted past the retention bound
// or gone after a restart) its report is rebuilt from store, so a finalized run
// stays reportable for as long as the store retains it.
type Server struct {
	mu      sync.Mutex
	specs   map[domain.ID]RunSpec
	runs    map[domain.ID]*runState
	store   store.Store
	adapter load.Adapter
	masker  *mask.Masker
	// annotateMu serializes the store read-modify-write in annotateRootCause.
	// Without it, two concurrent reproduce calls for different findings of the
	// same run could both read the finding list, each stamp only its own finding,
	// and the later SaveFindings would overwrite the first call's update — a
	// lost-update that silently drops one RootCauseClass from the system of
	// record. This lock is intentionally separate from mu and rs.mu so it does
	// not block unrelated store or run-state operations.
	annotateMu sync.Mutex
	// shareReg owns the share-token bookkeeping behind its own mutex, decoupled
	// from s.mu (which guards run state). Share access was already localized and
	// never shared a critical section with run state, so this is behavior-preserving.
	shareReg *shareRegistry
	// runOrder records run IDs in insertion order so the retention bound can evict
	// the oldest terminal runs first.
	runOrder       []domain.ID
	defaultWorkers []string
	// importFn, when set (WithImporter), converts an uploaded OpenAPI/HAR spec into
	// a RunSpec for POST /import. Injected so the api package avoids the
	// importer/scenariofile import cycle (both depend on api).
	importFn ImportFunc
	// importStatsFn, when set (WithImporterStats), is preferred over importFn and
	// additionally returns import coverage stats surfaced in the /import response.
	importStatsFn ImportStatsFunc
	seq           atomic.Int64
	now           func() time.Time
	mux           *http.ServeMux
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
		store:   store.NewMemStore(),
		adapter: adapter,
		masker:  mask.New(mask.Config{}),
		now:     time.Now,
		mux:     http.NewServeMux(),
	}
	// The registry reads s.now at call time (not a snapshot), so a test that later
	// reassigns s.now to drive share expiry is honored.
	s.shareReg = newShareRegistry(func() time.Time { return s.now() })
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
	s.mux.HandleFunc("POST /runs/{id}/reproduce", s.reproduceFinding)
	s.mux.HandleFunc("POST /runs/{id}/share", s.createShare)
	s.mux.HandleFunc("GET /reports/shared/{token}", s.getSharedReport)
	s.mux.HandleFunc("GET /capacity", s.getCapacity)
	s.mux.HandleFunc("POST /import", s.handleImport)
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
	if len(spec.Workers) == 0 && !spec.IsOpen() && spec.PoolSize() > maxLocalPoolUsers {
		return "", fmt.Errorf("api: closed pool of %d exceeds the in-process limit of %d; distribute across workers to run larger", spec.PoolSize(), maxLocalPoolUsers)
	}
	id := s.nextID("exp")
	spec.SetID(id)
	spec.Experiment.ID = id
	s.mu.Lock()
	s.specs[id] = spec
	s.mu.Unlock()
	return id, nil
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

// Report returns a finalized-or-live run's report and whether it was found. It is
// the Go-level accessor the in-process CLI polls (the HTTP report handler shares
// the same lookup), so an authenticated in-process run can be observed without a
// JSON round-trip.
func (s *Server) Report(id domain.ID) (Report, bool) { return s.reportFor(id) }
