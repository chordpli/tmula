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
	"sync"
	"sync/atomic"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/obs"
	"github.com/chordpli/tmula/internal/safety"
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
	if r.Start == "" {
		return fmt.Errorf("api: start node is required")
	}
	if len(r.Users) == 0 {
		return fmt.Errorf("api: at least one virtual user is required")
	}
	return nil
}

type runState struct {
	mu        sync.Mutex
	exec      domain.RunExecution
	collector *obs.Collector
	guard     *safety.Guard
	cancel    context.CancelFunc
	done      chan struct{}
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
	adapter load.Adapter
	seq     atomic.Int64
	now     func() time.Time
	mux     *http.ServeMux
}

// NewServer builds a control-plane server using the given adapter to reach the
// system under test.
func NewServer(adapter load.Adapter) *Server {
	s := &Server{
		specs:   make(map[domain.ID]RunSpec),
		runs:    make(map[domain.ID]*runState),
		adapter: adapter,
		now:     time.Now,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
}

// Handler exposes the control-plane routes.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("POST /experiments", s.createExperiment)
	s.mux.HandleFunc("GET /experiments/{id}", s.getExperiment)
	s.mux.HandleFunc("POST /experiments/{id}/run", s.runExperiment)
	s.mux.HandleFunc("POST /runs/{id}/kill", s.killRun)
	s.mux.HandleFunc("GET /runs/{id}/report", s.getReport)
	s.mux.HandleFunc("GET /runs/{id}/stream", s.streamRun)
}

func (s *Server) nextID(prefix string) domain.ID {
	return domain.ID(fmt.Sprintf("%s-%d", prefix, s.seq.Add(1)))
}

func (s *Server) createExperiment(w http.ResponseWriter, r *http.Request) {
	var spec RunSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode: %w", err))
		return
	}
	if err := spec.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
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

	runID := s.nextID("run")
	ctx, cancel := context.WithCancel(context.Background())
	rs := &runState{
		exec: domain.RunExecution{
			ID: runID, ExperimentID: id, Mode: domain.RunLocal,
			Status: domain.RunRunning, StartedAt: s.now(),
		},
		collector: obs.NewCollector(),
		guard:     guard,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	s.mu.Lock()
	s.runs[runID] = rs
	s.mu.Unlock()

	go s.execute(ctx, rs, spec)

	writeJSON(w, http.StatusAccepted, map[string]string{"runId": string(runID)})
}

func (s *Server) execute(ctx context.Context, rs *runState, spec RunSpec) {
	defer close(rs.done)
	runner := load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, spec.Templates)
	results, err := runner.Run(ctx, spec.Graph, spec.Start, spec.MaxSteps, spec.Users, spec.Seed)
	for _, res := range results {
		rs.collector.Record(res.Resp.StatusCode, res.Resp.LatencyMs, errorClass(res))
	}
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
	if rs.exec.Status == domain.RunRunning {
		rs.exec.Status = domain.RunKilled
		rs.exec.KillReason = reason
	}
	rs.mu.Unlock()
	rs.guard.Kill(reason)
	rs.cancel()
	writeJSON(w, http.StatusOK, map[string]string{"status": "killing"})
}

// Report is the operator-facing run report.
type Report struct {
	Run   domain.RunExecution `json:"run"`
	Stats obs.Stats           `json:"stats"`
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
	rs.mu.Lock()
	exec := rs.exec
	rs.mu.Unlock()
	writeJSON(w, http.StatusOK, Report{Run: exec, Stats: rs.collector.Snapshot()})
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

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	emit := func() {
		status, reason := rs.snapshotStatus()
		ev := map[string]any{"status": status, "reason": reason, "stats": rs.collector.Snapshot()}
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
