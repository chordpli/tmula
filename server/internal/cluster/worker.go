package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/cluster/clusterpb"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/chordpli/tmula/server/internal/safety"
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

// validateShardRequest checks the walk parameters that travel as authoritative
// proto fields rather than inside spec_json, so spec.Validate() never sees them:
// max_steps drives the walk length (0 would yield a degenerate single-node walk)
// and user_offset positions the shard's user range (a negative offset would name
// users below user-0 and misseed them). Rejecting them up front turns a silent
// degenerate run into a clear error.
func validateShardRequest(req *clusterpb.RunShardRequest) error {
	if req.GetMaxSteps() <= 0 {
		return fmt.Errorf("cluster: worker: maxSteps must be > 0, got %d", req.GetMaxSteps())
	}
	if req.GetUserOffset() < 0 {
		return fmt.Errorf("cluster: worker: userOffset must be >= 0, got %d", req.GetUserOffset())
	}
	return nil
}

// WorkerServer executes load shards on behalf of a master. It implements the
// generated clusterpb.ClusterServiceServer: RunShard builds a load.Runner from
// the decoded spec, runs the shard's slice of virtual users, and streams one
// ShardResult per request back to the master.
type WorkerServer struct {
	clusterpb.UnimplementedClusterServiceServer

	id      string
	adapter load.Adapter
	// credentialRoot is the directory a file-backed credential source is resolved
	// under on this worker. A shard's CredentialSourceRef names a path operator-
	// asserted to exist (identically ordered) on every worker host; this is where
	// the worker reads it from. Empty falls back to the process working directory.
	credentialRoot string
}

// WorkerOption customizes a WorkerServer.
type WorkerOption func(*WorkerServer)

// WithCredentialRoot sets the directory a file-backed credential source is
// resolved under. Worker hosts are secret-bearing: the referenced pool must be
// operator-asserted shared and identically ordered across every worker (the
// master-side checksum is the guard). An empty root uses the working directory.
func WithCredentialRoot(root string) WorkerOption {
	return func(w *WorkerServer) { w.credentialRoot = root }
}

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
	if err := validateShardRequest(req); err != nil {
		return err
	}
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
	users := buildUsers(offset, count)
	// Authenticate by GLOBAL index: when the spec carries a credential source the
	// worker resolves it locally and assigns users[i].Cred = Acquire(offset+i), so
	// the distributed pool authenticates exactly as the single-process path would.
	provider, perr := w.resolveProvider(stream.Context(), spec)
	if perr != nil {
		return perr
	}
	if aerr := authenticateUsers(stream.Context(), provider, users, offset); aerr != nil {
		return aerr
	}

	// Stream each result as it is produced instead of materializing the whole
	// shard: a result sink pushes every StepResult straight onto the gRPC stream,
	// so the worker never holds tens of millions of results in RAM at headline RPS.
	// A server stream's Send is NOT safe for concurrent calls and the sink fires
	// from many session goroutines at once, so every Send is serialized under
	// sendMu. The first Send failure is captured and the run context is cancelled
	// so the remaining sessions unwind promptly rather than sending into a broken
	// stream.
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	var (
		sendMu  sync.Mutex
		sendErr error
	)
	sink := func(r load.StepResult) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if sendErr != nil {
			return // already failed; drop the rest, the error is recorded
		}
		if err := stream.Send(toShardResult(r)); err != nil {
			sendErr = err
			cancel() // stop the other sessions; the first error is recorded
		}
	}
	// Deviation and think time arrive with the spec so each shard's sessions
	// walk and pace exactly as a local run would; both are no-ops at their zero
	// values.
	runner := load.NewRunner(w.adapter, spec.TargetBaseURL, spec.Templates,
		load.WithGuard(guard),
		load.WithCorrelationIDs(spec.RunID, spec.ScenarioID),
		load.WithResultSink(sink),
		load.WithDeviation(spec.DeviationRate),
		load.WithThinkTime(spec.ThinkTime),
	)

	// The Runner seeds user i (local) with seed+i. Offsetting the base seed by
	// the global offset makes local i correspond to global user offset+i, so the
	// per-user seed is exactly seed + global index regardless of the partition.
	if _, err := runner.Run(ctx, spec.Graph, start, int(req.GetMaxSteps()), users, req.GetSeed()+int64(offset)); err != nil {
		return fmt.Errorf("cluster: worker run shard: %w", err)
	}
	if sendErr != nil {
		return fmt.Errorf("cluster: worker stream result: %w", sendErr)
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
	if err := validateShardRequest(req); err != nil {
		return nil, err
	}
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
	users := buildUsers(offset, count)
	// Authenticate by GLOBAL index, identical to the streaming path: a credential
	// source is resolved locally and each user is assigned Acquire(offset+i).
	provider, perr := w.resolveProvider(ctx, spec)
	if perr != nil {
		return nil, perr
	}
	if aerr := authenticateUsers(ctx, provider, users, offset); aerr != nil {
		return nil, aerr
	}

	// Fold each result straight into the shard's Summary as it completes instead
	// of buffering the whole shard and summing afterward: Summary.Add is already
	// mutex-guarded, so it is a concurrency-safe ResultSink, and the worker's
	// memory stays flat (one fixed-size Summary) no matter how many requests the
	// shard makes.
	sink := func(r load.StepResult) { summary.Add(toObservation(r)) }
	// Deviation and think time ship with the spec, exactly as on the streaming
	// path, so the two reporting modes drive identical traffic.
	runner := load.NewRunner(w.adapter, spec.TargetBaseURL, spec.Templates,
		load.WithGuard(guard),
		load.WithCorrelationIDs(spec.RunID, spec.ScenarioID),
		load.WithResultSink(sink),
		load.WithDeviation(spec.DeviationRate),
		load.WithThinkTime(spec.ThinkTime),
	)

	if _, err := runner.Run(ctx, spec.Graph, start, int(req.GetMaxSteps()), users, req.GetSeed()+int64(offset)); err != nil {
		return nil, fmt.Errorf("cluster: worker run shard summary: %w", err)
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

// resolveProvider rebuilds the shard's credential provider from the spec's
// reference-only CredentialSource. A source-less spec returns (nil, nil) — the
// shard runs unauthenticated, exactly as before this seam existed. Otherwise it
// loads the referenced pool LOCALLY (the secrets never crossed the wire) and
// wraps it in a PoolProvider whose Acquire is a pure function of the GLOBAL
// index, so every worker reconstructs the identical assignment for its slice.
func (w *WorkerServer) resolveProvider(ctx context.Context, spec ShardSpec) (auth.Provider, error) {
	if spec.CredentialSource == nil {
		return nil, nil
	}
	src, err := auth.SourceFromRef(*spec.CredentialSource, w.credentialRoot)
	if err != nil {
		return nil, fmt.Errorf("cluster: worker resolve credential source: %w", err)
	}
	entries, err := src.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("cluster: worker load credential source: %w", err)
	}
	provider, err := auth.NewPoolProvider(entries)
	if err != nil {
		return nil, fmt.Errorf("cluster: worker build credential provider: %w", err)
	}
	return provider, nil
}

// SourceChecksum resolves the spec's credential source on this worker and returns
// a secret-free digest of the pool it loaded — over subjects, their order and the
// count, never the secrets (see auth.SourceChecksum). It is the cluster-side guard
// for the operator's "shared, identically-ordered source" assertion: every worker
// computes it against its OWN resolved pool, and a control plane comparing the
// digests across workers detects a divergent or differently-ordered source before
// it silently mis-assigns principals. A source-less spec returns "" (no pool).
func (w *WorkerServer) SourceChecksum(ctx context.Context, spec ShardSpec) (string, error) {
	if spec.CredentialSource == nil {
		return "", nil
	}
	src, err := auth.SourceFromRef(*spec.CredentialSource, w.credentialRoot)
	if err != nil {
		return "", fmt.Errorf("cluster: worker resolve credential source: %w", err)
	}
	entries, err := src.Load(ctx)
	if err != nil {
		return "", fmt.Errorf("cluster: worker load credential source: %w", err)
	}
	return auth.SourceChecksum(entries), nil
}

// authenticateUsers assigns each shard user the credential its GLOBAL index
// selects (users[i] is global index offset+i), so the worker authenticates by
// global index exactly as the single-process path does — the Acquire keying both
// run paths share. A nil provider leaves every user unauthenticated.
func authenticateUsers(ctx context.Context, provider auth.Provider, users []load.VirtualUser, offset int) error {
	if provider == nil {
		return nil
	}
	for i := range users {
		cred, err := provider.Acquire(ctx, offset+i)
		if err != nil {
			return fmt.Errorf("cluster: worker acquire credential for user %d: %w", offset+i, err)
		}
		users[i].Cred = cred
	}
	return nil
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
