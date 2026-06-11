// Package cluster implements distributed master-worker coordination for load
// runs. The same binary runs as a master (which splits a run's virtual users
// across registered workers and aggregates their results) or as a worker (which
// executes its assigned shard via the existing load.Runner and streams per-step
// results back). The wire contract lives in the generated clusterpb package.
package cluster

import (
	"encoding/json"
	"fmt"

	"github.com/chordpli/tmula/server/internal/domain"
)

// ShardSpec is the self-contained description of a run that the master ships to
// each worker (serialized as JSON into the proto's spec_json field). It carries
// everything a worker needs to build a load.Runner: the behavior graph, the API
// templates it references, the target base URL, and the walk parameters. The
// per-worker user partition (offset/count) travels in the proto, not here, so a
// single spec is shared verbatim by every shard.
type ShardSpec struct {
	// RunID and ScenarioID are stamped onto every outbound request so downstream
	// logs and traces can be filtered back to this tmula execution.
	RunID      domain.ID `json:"runId,omitempty"`
	ScenarioID domain.ID `json:"scenarioId,omitempty"`
	// Graph is the scenario graph every virtual user traverses.
	Graph domain.ScenarioGraph `json:"graph"`
	// Templates are the API templates the graph's nodes bind to, keyed by id.
	Templates map[domain.ID]domain.APITemplate `json:"templates"`
	// TargetBaseURL is the system-under-test base URL requests are sent to.
	TargetBaseURL string `json:"targetBaseUrl"`
	// Start is the node id every user's walk begins from.
	Start domain.ID `json:"start"`
	// MaxSteps bounds the length of each user's walk.
	MaxSteps int `json:"maxSteps"`
	// Seed is the run-wide base seed; a user's per-walk seed is Seed plus its
	// global index, which keeps the whole run deterministic across any split.
	Seed int64 `json:"seed"`
	// DeviationRate is the per-step probability (0..1) that a shard's virtual
	// user departs from the weighted happy path (the engine then abandons the
	// journey or explores an unlikely transition; dependency edges are never
	// violated). It ships with the spec so a distributed run deviates exactly
	// like a local one. 0 (the default) keeps the plain weighted walk.
	DeviationRate float64 `json:"deviationRate,omitempty"`
	// ThinkTime paces each shard user's steps: a uniform pause in [MinMs, MaxMs]
	// between consecutive requests, seeded per user like the traversal. The zero
	// value means no pause — the historical closed-model behavior.
	ThinkTime domain.ThinkTime `json:"thinkTime,omitempty"`

	// Allowlist, RateCap and EnvClass carry the control plane's safety policy so
	// a worker enforces the same host allowlist and rate/concurrency cap the
	// master does, on the actual TargetBaseURL it was handed. Empty Allowlist
	// means no policy was shipped (the worker then runs unguarded — only for
	// low-level tests; the control plane always populates these).
	Allowlist []string        `json:"allowlist,omitempty"`
	RateCap   domain.RateCap  `json:"rateCap,omitempty"`
	EnvClass  domain.EnvClass `json:"envClass,omitempty"`
}

// Validate checks the spec is runnable before it is dispatched or executed.
func (s ShardSpec) Validate() error {
	if err := s.Graph.Validate(); err != nil {
		return fmt.Errorf("cluster: shard spec graph: %w", err)
	}
	if s.TargetBaseURL == "" {
		return fmt.Errorf("cluster: shard spec: targetBaseUrl is required")
	}
	if s.Start == "" {
		return fmt.Errorf("cluster: shard spec: start node is required")
	}
	if s.MaxSteps <= 0 {
		return fmt.Errorf("cluster: shard spec: maxSteps must be > 0")
	}
	// Reject a malformed deviation rate or think range up front so a worker never
	// runs a silently skewed shard from a bad shipped policy.
	if s.DeviationRate < 0 || s.DeviationRate > 1 {
		return fmt.Errorf("cluster: shard spec: deviationRate %v out of range [0,1]", s.DeviationRate)
	}
	if err := s.ThinkTime.Validate(); err != nil {
		return fmt.Errorf("cluster: shard spec: %w", err)
	}
	// A shipped allowlist must come with a usable rate cap so the worker can
	// build the guard (NewGuard requires positive caps).
	if len(s.Allowlist) > 0 && (s.RateCap.MaxRPS <= 0 || s.RateCap.MaxConcurrency <= 0) {
		return fmt.Errorf("cluster: shard spec: rateCap must be positive when an allowlist is set")
	}
	return nil
}

// MarshalJSON-style helper: encodeSpec serializes a ShardSpec for the wire.
func encodeSpec(s ShardSpec) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("cluster: encode shard spec: %w", err)
	}
	return string(b), nil
}

// decodeSpec parses a wire spec_json back into a ShardSpec.
func decodeSpec(specJSON string) (ShardSpec, error) {
	var s ShardSpec
	if err := json.Unmarshal([]byte(specJSON), &s); err != nil {
		return ShardSpec{}, fmt.Errorf("cluster: decode shard spec: %w", err)
	}
	return s, nil
}

// shardAssignment is one worker's slice of the global virtual-user range:
// users [Offset, Offset+Count) named user-<global index>.
type shardAssignment struct {
	Offset int
	Count  int
}

// splitUsers partitions totalUsers across workerCount workers as evenly as
// possible, distributing any remainder one extra user at a time to the earliest
// workers. For example 10 users across 3 workers yields counts 4, 3, 3. Workers
// that would receive zero users are omitted, so callers never dispatch an empty
// shard. The returned assignments tile [0, totalUsers) with no gaps or overlap.
func splitUsers(totalUsers, workerCount int) []shardAssignment {
	if totalUsers <= 0 || workerCount <= 0 {
		return nil
	}
	base := totalUsers / workerCount
	remainder := totalUsers % workerCount

	out := make([]shardAssignment, 0, workerCount)
	offset := 0
	for i := 0; i < workerCount; i++ {
		count := base
		if i < remainder {
			count++
		}
		if count == 0 {
			continue // more workers than users: skip the empties
		}
		out = append(out, shardAssignment{Offset: offset, Count: count})
		offset += count
	}
	return out
}
