package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/chordpli/tmula/server/internal/cluster"
	"github.com/chordpli/tmula/server/internal/cluster/clusterpb"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// startWorker spins up a real WorkerServer on an ephemeral 127.0.0.1 port and
// returns its dial address plus a stop func. The worker reaches the SUT over
// REST exactly as it would in production.
func startWorker(t *testing.T) (addr string, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	clusterpb.RegisterClusterServiceServer(gs, cluster.NewWorkerServer())
	go func() { _ = gs.Serve(lis) }()
	return lis.Addr().String(), gs.GracefulStop
}

// TestDistributedRunAcrossWorkers exercises the full distributed control-plane
// path: a control-plane Server fans a run out across two real in-process gRPC
// workers, which drive traffic at an httptest SUT and stream results back. The
// run must aggregate every request across both workers.
func TestDistributedRunAcrossWorkers(t *testing.T) {
	var hits int32
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	w1, stop1 := startWorker(t)
	defer stop1()
	w2, stop2 := startWorker(t)
	defer stop2()

	cp, closeCP := newCP(t)
	defer closeCP()

	const users = 10
	const nodes = 2 // graph "a" -> "b"
	spec := specFor(sut.URL, users)
	spec.Workers = []string{w1, w2}

	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d", resp.StatusCode)
	}
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)

	if report.Run.Mode != domain.RunDistributed {
		t.Errorf("run mode = %q, want %q", report.Run.Mode, domain.RunDistributed)
	}
	if report.Workers != 2 {
		t.Errorf("report workers = %d, want 2", report.Workers)
	}
	if report.Stats.Total != users*nodes {
		t.Errorf("stats.Total = %d, want %d (users*nodes)", report.Stats.Total, users*nodes)
	}
	if report.Stats.Errors != 0 {
		t.Errorf("stats.Errors = %d, want 0", report.Stats.Errors)
	}
	// Every simulated request must have actually reached the SUT, proving the
	// shards ran end-to-end across both workers rather than being counted locally.
	if got := atomic.LoadInt32(&hits); int(got) != users*nodes {
		t.Errorf("SUT hits = %d, want %d", got, users*nodes)
	}
}

// TestDistributedSummaryRunAcrossWorkers exercises the worker-aggregated path:
// the same two-worker fan-out but with AggregateWorkers set, so each worker
// summarizes its shard and the master merges them. The merged run-wide stats
// must still account for every request the SUT served.
func TestDistributedSummaryRunAcrossWorkers(t *testing.T) {
	var hits int32
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	w1, stop1 := startWorker(t)
	defer stop1()
	w2, stop2 := startWorker(t)
	defer stop2()

	cp, closeCP := newCP(t)
	defer closeCP()

	const users = 10
	const nodes = 2
	spec := specFor(sut.URL, users)
	spec.Workers = []string{w1, w2}
	spec.AggregateWorkers = true

	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)
	if report.Stats.Total != users*nodes {
		t.Errorf("merged stats.Total = %d, want %d", report.Stats.Total, users*nodes)
	}
	if report.Stats.Errors != 0 {
		t.Errorf("merged stats.Errors = %d, want 0", report.Stats.Errors)
	}
	if got := atomic.LoadInt32(&hits); int(got) != users*nodes {
		t.Errorf("SUT hits = %d, want %d", got, users*nodes)
	}
}

// TestDistributedRunFailsOnUnreachableWorker asserts a dial/distribute failure
// surfaces as a failed run with the error recorded, leaving the local path's
// success semantics untouched.
func TestDistributedRunFailsOnUnreachableWorker(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	// Reserve a port and immediately release it so nothing is listening.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := lis.Addr().String()
	_ = lis.Close()

	spec := specFor(sut.URL, 4)
	spec.Workers = []string{dead}
	resp := postJSON(t, cp.URL+"/experiments", spec)
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunFailed, 5*time.Second)
	if report.Run.KillReason == "" {
		t.Error("expected a failure reason on the failed distributed run")
	}
}

// TestShardSpecForMapping pins the RunSpec -> cluster.ShardSpec mapping: every
// run-wide field a worker needs must cross, and the per-worker user partition
// must NOT (the Coordinator computes it from totalUsers).
func TestShardSpecForMapping(t *testing.T) {
	spec := RunSpec{
		Graph: domain.ScenarioGraph{
			ID:    "g",
			Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}},
			Edges: nil,
		},
		Templates: map[domain.ID]domain.APITemplate{"ta": {Method: "GET", Path: "/a"}},
		TargetEnv: domain.TargetEnv{BaseURL: "http://sut.example"},
		Start:     "a",
		MaxSteps:  7,
		Seed:      42,
		Users:     []load.VirtualUser{{ID: "u0"}, {ID: "u1"}},
		Workers:   []string{"w1", "w2"},
	}

	got := shardSpecFor(spec, "run-123")

	if got.RunID != "run-123" {
		t.Errorf("RunID = %q, want run-123", got.RunID)
	}
	if got.ScenarioID != "g" {
		t.Errorf("ScenarioID = %q, want graph id g", got.ScenarioID)
	}
	if got.TargetBaseURL != "http://sut.example" {
		t.Errorf("TargetBaseURL = %q, want from TargetEnv.BaseURL", got.TargetBaseURL)
	}
	if got.Start != "a" {
		t.Errorf("Start = %q, want a", got.Start)
	}
	if got.MaxSteps != 7 {
		t.Errorf("MaxSteps = %d, want 7", got.MaxSteps)
	}
	if got.Seed != 42 {
		t.Errorf("Seed = %d, want 42", got.Seed)
	}
	if got.Graph.ID != "g" || len(got.Graph.Nodes) != 1 {
		t.Errorf("Graph not carried through: %+v", got.Graph)
	}
	if tpl, ok := got.Templates["ta"]; !ok || tpl.Path != "/a" {
		t.Errorf("Templates not carried through: %+v", got.Templates)
	}
	// The mapped spec must validate (it is what ships to every worker).
	if err := got.Validate(); err != nil {
		t.Errorf("mapped ShardSpec invalid: %v", err)
	}
}

// TestShardSpecForCarriesCredentialSource pins the distributed-auth mapping: a
// source-backed credential pool copies its reference-only CredentialSourceRef
// into the ShardSpec (so each worker resolves it locally and assigns by global
// index), while an inline-entries pool and a nil pool leave it nil — no secret is
// ever placed on the wire spec.
func TestShardSpecForCarriesCredentialSource(t *testing.T) {
	base := func() RunSpec {
		return RunSpec{
			Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}}},
			Templates: map[domain.ID]domain.APITemplate{"ta": {Method: "GET", Path: "/a"}},
			TargetEnv: domain.TargetEnv{BaseURL: "http://sut.example"},
			Start:     "a",
			MaxSteps:  3,
			Seed:      1,
			Workers:   []string{"w1"},
		}
	}

	t.Run("source pool copies the reference", func(t *testing.T) {
		spec := base()
		spec.CredentialPool = &domain.CredentialPool{
			ID:       "p",
			Strategy: domain.CredPool,
			Source:   &domain.CredentialSourceRef{File: "creds.csv", Format: "csv"},
		}
		got := shardSpecFor(spec, "run-1")
		if got.CredentialSource == nil {
			t.Fatal("a source pool must copy its reference into the shard spec")
		}
		if got.CredentialSource.File != "creds.csv" || got.CredentialSource.Format != "csv" {
			t.Errorf("source reference not copied faithfully: %+v", got.CredentialSource)
		}
		// The copy must be defensive: mutating the spec's ref must not change the
		// shard's, and vice versa.
		spec.CredentialPool.Source.File = "other.csv"
		if got.CredentialSource.File != "creds.csv" {
			t.Error("shard spec credential source must be a defensive copy")
		}
	})

	t.Run("inline pool leaves the source nil", func(t *testing.T) {
		spec := base()
		spec.CredentialPool = &domain.CredentialPool{
			ID:       "p",
			Strategy: domain.CredPool,
			Entries:  []domain.Credential{{Subject: "u0", Secret: "tok-0"}},
		}
		if got := shardSpecFor(spec, "run-1"); got.CredentialSource != nil {
			t.Errorf("an inline pool must not place a source on the wire, got: %+v", got.CredentialSource)
		}
	})

	t.Run("nil pool leaves the source nil", func(t *testing.T) {
		if got := shardSpecFor(base(), "run-1"); got.CredentialSource != nil {
			t.Errorf("a nil pool must leave the shard source nil, got: %+v", got.CredentialSource)
		}
	})
}
