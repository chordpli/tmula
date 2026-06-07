package cluster

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/chordpli/tmula/internal/cluster/clusterpb"
	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
)

// linearSpec builds a two-node linear scenario (n1 -> n2) where both nodes call
// the SUT, so every virtual user makes exactly len(nodes) == 2 requests.
func linearSpec(baseURL string) ShardSpec {
	tmpl1 := domain.APITemplate{ID: "t1", Protocol: domain.ProtocolREST, Method: http.MethodGet, Path: "/one"}
	tmpl2 := domain.APITemplate{ID: "t2", Protocol: domain.ProtocolREST, Method: http.MethodGet, Path: "/two"}
	return ShardSpec{
		Graph: domain.ScenarioGraph{
			ID: "g1",
			Nodes: []domain.Node{
				{ID: "n1", APITemplateID: "t1"},
				{ID: "n2", APITemplateID: "t2"},
			},
			Edges: []domain.Edge{{From: "n1", To: "n2", Weight: 1}},
		},
		Templates:     map[domain.ID]domain.APITemplate{"t1": tmpl1, "t2": tmpl2},
		TargetBaseURL: baseURL,
		Start:         "n1",
		MaxSteps:      5,
		Seed:          1,
	}
}

// startWorker spins a WorkerServer on an in-process bufconn listener and returns
// a dialed client connection. The server and listener are torn down via t.Cleanup.
func startWorker(t *testing.T, opts ...WorkerOption) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	clusterpb.RegisterClusterServiceServer(srv, NewWorkerServer(opts...))

	go func() {
		// Serve returns when the listener is closed in cleanup; ignore that error.
		_ = srv.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return conn
}

// TestDistributeAggregatesAcrossWorkers is the end-to-end path: two bufconn
// workers, an httptest SUT returning 200, 10 users over a 2-node graph. The
// aggregated stats must show every request (users*nodes) with zero errors, and
// the SUT must have observed exactly that many hits.
func TestDistributeAggregatesAcrossWorkers(t *testing.T) {
	t.Parallel()

	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sut.Close)

	const users = 10
	const nodes = 2

	adapter := load.NewRESTAdapter(5 * time.Second)
	conn1 := startWorker(t, WithWorkerID("w1"), WithAdapter(adapter))
	conn2 := startWorker(t, WithWorkerID("w2"), WithAdapter(adapter))

	coord, err := NewCoordinator(conn1, conn2)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stats, steps, err := coord.Distribute(ctx, linearSpec(sut.URL), users)
	if err != nil {
		t.Fatalf("distribute: %v", err)
	}

	if want := users * nodes; stats.Total != want {
		t.Fatalf("stats.Total = %d, want %d", stats.Total, want)
	}
	if stats.Errors != 0 {
		t.Fatalf("stats.Errors = %d, want 0", stats.Errors)
	}
	if len(steps) != users*nodes {
		t.Fatalf("len(steps) = %d, want %d", len(steps), users*nodes)
	}
	if got := atomic.LoadInt64(&hits); got != int64(users*nodes) {
		t.Fatalf("SUT hits = %d, want %d", got, users*nodes)
	}
	if got := stats.StatusCounts[http.StatusOK]; got != users*nodes {
		t.Fatalf("stats.StatusCounts[200] = %d, want %d", got, users*nodes)
	}
}

// TestDistributeSingleWorker covers the degenerate one-worker fan-out: all users
// land on a single shard and still aggregate correctly.
func TestDistributeSingleWorker(t *testing.T) {
	t.Parallel()

	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sut.Close)

	conn := startWorker(t, WithAdapter(load.NewRESTAdapter(5*time.Second)))
	coord, err := NewCoordinator(conn)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const users = 7
	stats, _, err := coord.Distribute(ctx, linearSpec(sut.URL), users)
	if err != nil {
		t.Fatalf("distribute: %v", err)
	}
	if want := users * 2; stats.Total != want || stats.Errors != 0 {
		t.Fatalf("stats = {Total:%d Errors:%d}, want {Total:%d Errors:0}", stats.Total, stats.Errors, want)
	}
}

// TestDistributeSummaryAcrossWorkers covers the worker-aggregated path: two
// bufconn workers each summarize their own shard and the master merges the two
// summaries into run-wide stats — with no per-request stream — which must match
// the full request volume with zero errors and the right status tally.
func TestDistributeSummaryAcrossWorkers(t *testing.T) {
	t.Parallel()

	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sut.Close)

	const users = 10
	const nodes = 2
	adapter := load.NewRESTAdapter(5 * time.Second)
	coord, err := NewCoordinator(
		startWorker(t, WithWorkerID("w1"), WithAdapter(adapter)),
		startWorker(t, WithWorkerID("w2"), WithAdapter(adapter)),
	)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	summary, err := coord.DistributeSummary(ctx, linearSpec(sut.URL), users)
	if err != nil {
		t.Fatalf("distribute summary: %v", err)
	}
	stats := summary.Stats()
	if want := users * nodes; stats.Total != want {
		t.Fatalf("merged Total = %d, want %d", stats.Total, want)
	}
	if stats.Errors != 0 {
		t.Fatalf("merged Errors = %d, want 0", stats.Errors)
	}
	if got := stats.StatusCounts[http.StatusOK]; got != users*nodes {
		t.Fatalf("merged StatusCounts[200] = %d, want %d", got, users*nodes)
	}
	if got := atomic.LoadInt64(&hits); got != int64(users*nodes) {
		t.Fatalf("SUT hits = %d, want %d", got, users*nodes)
	}
}

// TestWorkerEnforcesAllowlist: a worker handed a shard whose allowlist excludes
// the target host blocks every request — the SUT receives no traffic and the
// aggregated stats record the blocks as errors.
func TestWorkerEnforcesAllowlist(t *testing.T) {
	t.Parallel()

	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sut.Close)

	conn := startWorker(t, WithAdapter(load.NewRESTAdapter(2*time.Second)))
	coord, err := NewCoordinator(conn)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	spec := linearSpec(sut.URL)
	spec.Allowlist = []string{"example.com"} // excludes the SUT host
	spec.RateCap = domain.RateCap{MaxRPS: 1000, MaxConcurrency: 100}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stats, _, err := coord.Distribute(ctx, spec, 5)
	if err != nil {
		t.Fatalf("distribute: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Errorf("SUT hits = %d, want 0 (worker must block the off-allowlist host)", got)
	}
	if stats.Errors == 0 {
		t.Error("blocked requests should be recorded as errors")
	}
}

// TestWorkerPing covers the health RPC over bufconn.
func TestWorkerPing(t *testing.T) {
	t.Parallel()

	conn := startWorker(t, WithWorkerID("worker-42"))
	client := clusterpb.NewClusterServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reply, err := client.Ping(ctx, &clusterpb.PingRequest{Nonce: "ping-123"})
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if reply.GetNonce() != "ping-123" {
		t.Fatalf("ping nonce = %q, want %q", reply.GetNonce(), "ping-123")
	}
	if reply.GetWorkerId() != "worker-42" {
		t.Fatalf("ping workerId = %q, want %q", reply.GetWorkerId(), "worker-42")
	}
}

// TestSplitUsers checks the pure partitioning helper, including the uneven case
// from the issue (10 users / 3 workers -> 4,3,3) and edge conditions.
func TestSplitUsers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		total      int
		workers    int
		wantCounts []int
	}{
		{"even", 10, 2, []int{5, 5}},
		{"uneven 10/3", 10, 3, []int{4, 3, 3}},
		{"single worker", 10, 1, []int{10}},
		{"more workers than users", 2, 5, []int{1, 1}},
		{"one each", 3, 3, []int{1, 1, 1}},
		{"zero users", 0, 3, nil},
		{"zero workers", 5, 0, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitUsers(tc.total, tc.workers)

			if len(got) != len(tc.wantCounts) {
				t.Fatalf("got %d assignments, want %d (%v)", len(got), len(tc.wantCounts), got)
			}

			sum, expectOffset := 0, 0
			for i, a := range got {
				if a.Count != tc.wantCounts[i] {
					t.Errorf("assignment %d count = %d, want %d", i, a.Count, tc.wantCounts[i])
				}
				if a.Offset != expectOffset {
					t.Errorf("assignment %d offset = %d, want %d (no gaps/overlap)", i, a.Offset, expectOffset)
				}
				expectOffset += a.Count
				sum += a.Count
			}
			// Assignments must tile exactly [0,total) when any users are assigned.
			if len(tc.wantCounts) > 0 && sum != tc.total {
				t.Errorf("assigned %d users, want %d", sum, tc.total)
			}
		})
	}
}

// TestNewCoordinatorRequiresWorker rejects an empty worker set.
func TestNewCoordinatorRequiresWorker(t *testing.T) {
	t.Parallel()
	if _, err := NewCoordinator(); err == nil {
		t.Fatal("expected error for zero workers, got nil")
	}
}
