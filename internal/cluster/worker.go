package cluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"

	"github.com/chordpli/tmula/internal/cluster/clusterpb"
	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/obs"
	"github.com/chordpli/tmula/internal/safety"
)

// guardForSpec builds the safety guard for a shard from the policy the master
// shipped in the spec (host allowlist + rate/concurrency cap), so a worker
// enforces the same allowlist the control plane does — on the actual
// TargetBaseURL it was handed — rather than trusting it blindly. An empty
// allowlist means no policy was shipped (low-level tests), and the worker then
// runs unguarded.
func guardForSpec(spec ShardSpec) (*safety.Guard, error) {
	if len(spec.Allowlist) == 0 {
		return nil, nil
	}
	return safety.NewGuard(safety.Config{
		Allowlist:      spec.Allowlist,
		MaxRPS:         spec.RateCap.MaxRPS,
		MaxConcurrency: spec.RateCap.MaxConcurrency,
	})
}

// defaultRequestTimeout bounds each individual request a worker makes when the
// caller does not supply a custom adapter.
const defaultRequestTimeout = 30 * time.Second

// WorkerServer executes load shards on behalf of a master. It implements the
// generated clusterpb.ClusterServiceServer: RunShard builds a load.Runner from
// the decoded spec, runs the shard's slice of virtual users, and streams one
// ShardResult per request back to the master.
type WorkerServer struct {
	clusterpb.UnimplementedClusterServiceServer

	id      string
	adapter load.Adapter
}

// WorkerOption customizes a WorkerServer.
type WorkerOption func(*WorkerServer)

// WithWorkerID sets the worker's identity, echoed in Ping replies and useful in
// logs when several workers serve one run.
func WithWorkerID(id string) WorkerOption {
	return func(w *WorkerServer) { w.id = id }
}

// WithAdapter overrides the load.Adapter the worker uses to reach the system
// under test. The default is a RESTAdapter with a 30s per-request timeout.
// Tests use this to inject an adapter pointed at an httptest server.
func WithAdapter(a load.Adapter) WorkerOption {
	return func(w *WorkerServer) { w.adapter = a }
}

// NewWorkerServer builds a WorkerServer. By default it sends traffic over REST.
func NewWorkerServer(opts ...WorkerOption) *WorkerServer {
	w := &WorkerServer{adapter: load.NewRESTAdapter(defaultRequestTimeout)}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Ping reports liveness, echoing the request nonce and the worker's id.
func (w *WorkerServer) Ping(_ context.Context, req *clusterpb.PingRequest) (*clusterpb.PingReply, error) {
	return &clusterpb.PingReply{Nonce: req.GetNonce(), WorkerId: w.id}, nil
}

// RunShard executes this worker's assigned slice of virtual users and streams a
// ShardResult for every request they make. The shard owns the global user range
// [user_offset, user_offset+user_count); each user is named user-<global index>
// and its walk is seeded with spec.Seed + global index, so the run is
// deterministic no matter how the master partitioned the users. Per-request
// failures are streamed as results (with an error_class) rather than aborting
// the shard; the stream ends when every user finishes or the context is done.
func (w *WorkerServer) RunShard(req *clusterpb.RunShardRequest, stream grpc.ServerStreamingServer[clusterpb.ShardResult]) error {
	spec, err := decodeSpec(req.GetSpecJson())
	if err != nil {
		return err
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	count := int(req.GetUserCount())
	if count <= 0 {
		return nil // empty shard: nothing to run, clean stream close
	}
	offset := int(req.GetUserOffset())

	// Walk parameters travel as authoritative proto fields (start_node, seed,
	// max_steps); spec_json supplies the graph, templates and target. The master
	// fills the proto fields from the same spec, so the two never diverge.
	start := domain.ID(req.GetStartNode())
	guard, gerr := guardForSpec(spec)
	if gerr != nil {
		return fmt.Errorf("cluster: worker build guard: %w", gerr)
	}
	runner := load.NewRunner(w.adapter, spec.TargetBaseURL, spec.Templates, load.WithGuard(guard))
	users := buildUsers(offset, count)

	// The Runner seeds user i (local) with seed+i. Offsetting the base seed by
	// the global offset makes local i correspond to global user offset+i, so the
	// per-user seed is exactly seed + global index regardless of the partition.
	results, err := runner.Run(stream.Context(), spec.Graph, start, int(req.GetMaxSteps()), users, req.GetSeed()+int64(offset))
	if err != nil {
		return fmt.Errorf("cluster: worker run shard: %w", err)
	}

	for i := range results {
		if sendErr := stream.Send(toShardResult(results[i])); sendErr != nil {
			return fmt.Errorf("cluster: worker stream result: %w", sendErr)
		}
	}
	return nil
}

// RunShardSummary executes the same shard as RunShard but aggregates every
// request into one obs.Summary on the worker and returns it as a single
// ShardSummary, instead of streaming a message per request. The users, seeding
// and partition are identical to RunShard — only the reporting differs — so the
// master can fold per-worker summaries into run-wide stats at a fixed network and
// memory cost regardless of request volume.
func (w *WorkerServer) RunShardSummary(ctx context.Context, req *clusterpb.RunShardRequest) (*clusterpb.ShardSummary, error) {
	spec, err := decodeSpec(req.GetSpecJson())
	if err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	summary := obs.NewSummary()
	count := int(req.GetUserCount())
	if count <= 0 {
		return toShardSummary(summary), nil // empty shard: an empty (mergeable) summary
	}
	offset := int(req.GetUserOffset())
	start := domain.ID(req.GetStartNode())
	guard, gerr := guardForSpec(spec)
	if gerr != nil {
		return nil, fmt.Errorf("cluster: worker build guard: %w", gerr)
	}
	runner := load.NewRunner(w.adapter, spec.TargetBaseURL, spec.Templates, load.WithGuard(guard))
	users := buildUsers(offset, count)

	results, err := runner.Run(ctx, spec.Graph, start, int(req.GetMaxSteps()), users, req.GetSeed()+int64(offset))
	if err != nil {
		return nil, fmt.Errorf("cluster: worker run shard summary: %w", err)
	}
	for i := range results {
		summary.Add(toObservation(results[i]))
	}
	return toShardSummary(summary), nil
}

// toObservation maps a load step result onto the observation the Summary tallies,
// deriving the error class exactly as the streaming path does. The cluster path
// has no input mutation, so Mutated is always false; the Summary ignores TS.
func toObservation(r load.StepResult) obs.RequestObservation {
	return obs.RequestObservation{
		APIID:      r.NodeID,
		StatusCode: r.Resp.StatusCode,
		LatencyMs:  r.Resp.LatencyMs,
		ErrorClass: errorClass(r.Err),
	}
}

// toShardSummary serializes a worker's Summary into its wire form, mapping the
// obs maps onto the proto's keyed counts.
func toShardSummary(s *obs.Summary) *clusterpb.ShardSummary {
	d := s.Export()
	status := make(map[int32]int64, len(d.StatusCounts))
	for code, n := range d.StatusCounts {
		status[int32(code)] = int64(n)
	}
	findings := make(map[string]int64, len(d.FindingCounts))
	for cat, n := range d.FindingCounts {
		findings[string(cat)] = int64(n)
	}
	return &clusterpb.ShardSummary{
		Total:         int64(d.Total),
		Errors:        int64(d.Errors),
		Timeouts:      int64(d.Timeouts),
		StatusCounts:  status,
		FindingCounts: findings,
		HistBuckets:   d.HistBuckets,
		HistMax:       d.HistMax,
	}
}

// buildUsers materializes the virtual users for a shard. IDs are globally
// stable (user-<global index>) so aggregation and seeding are independent of how
// users were split across workers.
func buildUsers(offset, count int) []load.VirtualUser {
	users := make([]load.VirtualUser, count)
	for i := 0; i < count; i++ {
		users[i] = load.VirtualUser{ID: fmt.Sprintf("user-%d", offset+i)}
	}
	return users
}

// toShardResult converts a load.StepResult into its wire form, deriving the
// error class the same way the local runner path does (a transport error, or a
// timeout when the failure is a deadline/cancellation).
func toShardResult(r load.StepResult) *clusterpb.ShardResult {
	return &clusterpb.ShardResult{
		UserId:     r.UserID,
		ApiId:      string(r.NodeID),
		StatusCode: int32(r.Resp.StatusCode),
		LatencyMs:  r.Resp.LatencyMs,
		ErrorClass: errorClass(r.Err),
	}
}

// errorClass maps a step error to a stable class string understood by
// obs.Collector. A nil error is success (""); a deadline/cancellation is a
// timeout; anything else is a transport failure.
func errorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return obs.TimeoutClass
	default:
		return "transport"
	}
}
