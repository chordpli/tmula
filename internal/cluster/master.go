package cluster

import (
	"context"
	"fmt"
	"io"
	"sync"

	"google.golang.org/grpc"

	"github.com/chordpli/tmula/internal/cluster/clusterpb"
	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/obs"
)

// Coordinator is the master side of a distributed run. It fans a run's virtual
// users out across a fixed set of worker connections, drives RunShard on each
// concurrently, and aggregates every streamed result into one obs.Collector.
type Coordinator struct {
	workers []clusterpb.ClusterServiceClient
}

// NewCoordinator builds a Coordinator over the given worker gRPC connections.
// It accepts grpc.ClientConnInterface so callers can pass real *grpc.ClientConn
// instances or in-process bufconn dials interchangeably. At least one worker is
// required.
func NewCoordinator(conns ...grpc.ClientConnInterface) (*Coordinator, error) {
	if len(conns) == 0 {
		return nil, fmt.Errorf("cluster: coordinator needs at least one worker")
	}
	clients := make([]clusterpb.ClusterServiceClient, 0, len(conns))
	for _, cc := range conns {
		if cc == nil {
			return nil, fmt.Errorf("cluster: coordinator: nil worker connection")
		}
		clients = append(clients, clusterpb.NewClusterServiceClient(cc))
	}
	return &Coordinator{workers: clients}, nil
}

// NewCoordinatorFromClients builds a Coordinator from already-constructed
// service clients. It is the seam tests use to inject fakes.
func NewCoordinatorFromClients(clients ...clusterpb.ClusterServiceClient) (*Coordinator, error) {
	if len(clients) == 0 {
		return nil, fmt.Errorf("cluster: coordinator needs at least one worker")
	}
	cp := &Coordinator{workers: make([]clusterpb.ClusterServiceClient, len(clients))}
	copy(cp.workers, clients)
	return cp, nil
}

// WorkerCount reports how many workers the Coordinator will distribute across.
func (c *Coordinator) WorkerCount() int { return len(c.workers) }

// Distribute splits totalUsers across the registered workers, runs each shard
// concurrently, and aggregates every streamed result into a single Collector.
// It returns the aggregated obs.Stats together with every load step result (one
// per request made across all workers), so callers can both summarize and
// inspect the raw stream. Distribute blocks until all shards complete; if any
// worker fails, it returns the first error after the others have been allowed to
// finish, so no shard goroutine is leaked.
//
// This form materializes every step for the caller; a master folding a
// millions-request run into an aggregator should prefer DistributeInto, which
// streams each step to a sink and never buffers the whole run.
func (c *Coordinator) Distribute(ctx context.Context, spec ShardSpec, totalUsers int) (obs.Stats, []ShardStep, error) {
	var (
		mu    sync.Mutex
		steps []ShardStep
	)
	// The accumulating sink guards the slice because DistributeInto invokes it
	// concurrently from every shard's stream.
	stats, err := c.DistributeInto(ctx, spec, totalUsers, func(s ShardStep) {
		mu.Lock()
		steps = append(steps, s)
		mu.Unlock()
	})
	if err != nil {
		return obs.Stats{}, nil, err
	}
	return stats, steps, nil
}

// DistributeInto is Distribute without the per-run buffer: it streams each shard
// step to sink as it arrives and returns only the aggregated Collector stats, so
// a master can fold a run of arbitrary size into an aggregator at bounded memory
// instead of accumulating one ShardStep per request for the whole run. sink is
// called concurrently from every shard's receive loop, so it must be safe for
// concurrent use. It is the master-side counterpart to the worker's ResultSink.
// Like Distribute it blocks until all shards finish and returns the first worker
// error after the rest unwind.
func (c *Coordinator) DistributeInto(ctx context.Context, spec ShardSpec, totalUsers int, sink func(ShardStep)) (obs.Stats, error) {
	if err := spec.Validate(); err != nil {
		return obs.Stats{}, err
	}
	if totalUsers <= 0 {
		return obs.Stats{}, fmt.Errorf("cluster: distribute: totalUsers must be > 0")
	}

	specJSON, err := encodeSpec(spec)
	if err != nil {
		return obs.Stats{}, err
	}

	// Cancel sibling shards as soon as one fails so a doomed distributed run stops
	// hammering the SUT instead of letting the healthy workers run to completion.
	// Workers honor the streamed context, so cancellation propagates to them.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	assignments := splitUsers(totalUsers, len(c.workers))
	collector := obs.NewCollector()

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	failOnce := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		// Stop the other shards; the first error is already recorded.
		cancel()
	}

	for i, a := range assignments {
		wg.Add(1)
		go func(idx int, worker clusterpb.ClusterServiceClient, a shardAssignment) {
			defer wg.Done()
			req := &clusterpb.RunShardRequest{
				SpecJson:   specJSON,
				UserOffset: int32(a.Offset),
				UserCount:  int32(a.Count),
				Seed:       spec.Seed,
				MaxSteps:   int32(spec.MaxSteps),
				StartNode:  string(spec.Start),
			}
			if err := runShard(ctx, idx, worker, req, collector, sink); err != nil {
				failOnce(err)
			}
		}(i, c.workers[i], a)
	}
	wg.Wait()

	if firstErr != nil {
		return obs.Stats{}, firstErr
	}
	return collector.Snapshot(), nil
}

// DistributeSummary fans the run's users across workers like Distribute, but
// asks each worker to aggregate its shard and return a single ShardSummary
// rather than streaming every request. It merges the per-worker summaries into
// one and returns it, so the caller recovers run-wide stats (and finding
// tallies) at a fixed network and memory cost no matter how many requests the
// run makes — the path that scales to millions. Like Distribute it blocks until
// every shard completes and returns the first worker error, if any.
func (c *Coordinator) DistributeSummary(ctx context.Context, spec ShardSpec, totalUsers int) (*obs.Summary, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if totalUsers <= 0 {
		return nil, fmt.Errorf("cluster: distribute summary: totalUsers must be > 0")
	}
	specJSON, err := encodeSpec(spec)
	if err != nil {
		return nil, err
	}

	// Cancel sibling shards on the first failure so a doomed run stops loading the
	// SUT rather than letting the healthy workers finish. Workers honor ctx.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	assignments := splitUsers(totalUsers, len(c.workers))
	var (
		mu        sync.Mutex
		summaries []*obs.Summary
		firstErr  error
		wg        sync.WaitGroup
	)
	for i, a := range assignments {
		wg.Add(1)
		go func(worker clusterpb.ClusterServiceClient, a shardAssignment) {
			defer wg.Done()
			req := &clusterpb.RunShardRequest{
				SpecJson:   specJSON,
				UserOffset: int32(a.Offset),
				UserCount:  int32(a.Count),
				Seed:       spec.Seed,
				MaxSteps:   int32(spec.MaxSteps),
				StartNode:  string(spec.Start),
			}
			ps, err := worker.RunShardSummary(ctx, req)
			if err == nil {
				var sum *obs.Summary
				sum, err = fromShardSummary(ps)
				if err == nil {
					mu.Lock()
					summaries = append(summaries, sum)
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			if firstErr == nil {
				firstErr = fmt.Errorf("cluster: run shard summary: %w", err)
			}
			mu.Unlock()
			// Stop the other shards; the first error is already recorded.
			cancel()
		}(c.workers[i], a)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	// Merge sequentially after the barrier: simple and obviously correct, and the
	// per-shard summaries are already bounded in size.
	merged := obs.NewSummary()
	for _, s := range summaries {
		merged.Merge(s)
	}
	return merged, nil
}

// fromShardSummary rebuilds a mergeable obs.Summary from a worker's wire summary.
func fromShardSummary(ps *clusterpb.ShardSummary) (*obs.Summary, error) {
	status := make(map[int]int, len(ps.GetStatusCounts()))
	for code, n := range ps.GetStatusCounts() {
		status[int(code)] = int(n)
	}
	findings := make(map[domain.FindingCategory]int, len(ps.GetFindingCounts()))
	for cat, n := range ps.GetFindingCounts() {
		findings[domain.FindingCategory(cat)] = int(n)
	}
	return obs.LoadSummary(obs.SummaryData{
		Total:         int(ps.GetTotal()),
		Errors:        int(ps.GetErrors()),
		Timeouts:      int(ps.GetTimeouts()),
		StatusCounts:  status,
		FindingCounts: findings,
		HistBuckets:   ps.GetHistBuckets(),
		HistMax:       ps.GetHistMax(),
	})
}

// ShardStep is one request outcome reported by a worker, surfaced to the caller
// of Distribute alongside the aggregated stats.
type ShardStep struct {
	WorkerIndex int
	UserID      string
	APIID       string
	StatusCode  int
	LatencyMs   float64
	ErrorClass  string
}

// runShard drives one worker's RunShard stream to completion, folding every
// result into the shared collector and handing it to sink as it arrives — it
// never buffers the shard. The collector is concurrency-safe, so many shards may
// record into it in parallel; sink is shared across shards too and so must be
// concurrency-safe (its callers ensure that).
func runShard(
	ctx context.Context,
	workerIndex int,
	worker clusterpb.ClusterServiceClient,
	req *clusterpb.RunShardRequest,
	collector *obs.Collector,
	sink func(ShardStep),
) error {
	stream, err := worker.RunShard(ctx, req)
	if err != nil {
		return fmt.Errorf("cluster: open shard stream: %w", err)
	}
	for {
		res, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("cluster: receive shard result: %w", err)
		}
		collector.Record(int(res.GetStatusCode()), res.GetLatencyMs(), res.GetErrorClass())
		sink(ShardStep{
			WorkerIndex: workerIndex,
			UserID:      res.GetUserId(),
			APIID:       res.GetApiId(),
			StatusCode:  int(res.GetStatusCode()),
			LatencyMs:   res.GetLatencyMs(),
			ErrorClass:  res.GetErrorClass(),
		})
	}
}
