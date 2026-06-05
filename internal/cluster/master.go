package cluster

import (
	"context"
	"fmt"
	"io"
	"sync"

	"google.golang.org/grpc"

	"github.com/chordpli/tmula/internal/cluster/clusterpb"
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
func (c *Coordinator) Distribute(ctx context.Context, spec ShardSpec, totalUsers int) (obs.Stats, []ShardStep, error) {
	if err := spec.Validate(); err != nil {
		return obs.Stats{}, nil, err
	}
	if totalUsers <= 0 {
		return obs.Stats{}, nil, fmt.Errorf("cluster: distribute: totalUsers must be > 0")
	}

	specJSON, err := encodeSpec(spec)
	if err != nil {
		return obs.Stats{}, nil, err
	}

	assignments := splitUsers(totalUsers, len(c.workers))
	collector := obs.NewCollector()

	var (
		mu       sync.Mutex
		steps    []ShardStep
		firstErr error
		wg       sync.WaitGroup
	)
	record := func(s ShardStep) {
		mu.Lock()
		steps = append(steps, s)
		mu.Unlock()
	}
	failOnce := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
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
			if err := runShard(ctx, idx, worker, req, collector, record); err != nil {
				failOnce(err)
			}
		}(i, c.workers[i], a)
	}
	wg.Wait()

	if firstErr != nil {
		return obs.Stats{}, nil, firstErr
	}
	return collector.Snapshot(), steps, nil
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

// runShard drives one worker's RunShard stream to completion, recording every
// result into the shared collector and the caller's sink. The collector is
// concurrency-safe, so many shards may record into it in parallel.
func runShard(
	ctx context.Context,
	workerIndex int,
	worker clusterpb.ClusterServiceClient,
	req *clusterpb.RunShardRequest,
	collector *obs.Collector,
	record func(ShardStep),
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
		record(ShardStep{
			WorkerIndex: workerIndex,
			UserID:      res.GetUserId(),
			APIID:       res.GetApiId(),
			StatusCode:  int(res.GetStatusCode()),
			LatencyMs:   res.GetLatencyMs(),
			ErrorClass:  res.GetErrorClass(),
		})
	}
}
