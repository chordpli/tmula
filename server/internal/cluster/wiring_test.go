package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// TestShardSpecRoundTripsDeviationAndThink pins the wire contract: the master's
// deviation rate and closed-model think time survive the spec_json round trip,
// so a worker's sessions behave exactly like local ones.
func TestShardSpecRoundTripsDeviationAndThink(t *testing.T) {
	t.Parallel()

	spec := linearSpec("http://t")
	spec.DeviationRate = 0.35
	spec.ThinkTime = domain.ThinkTime{MinMs: 10, MaxMs: 20}

	enc, err := encodeSpec(spec)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	dec, err := decodeSpec(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.DeviationRate != 0.35 {
		t.Errorf("deviationRate = %v, want 0.35", dec.DeviationRate)
	}
	if dec.ThinkTime != (domain.ThinkTime{MinMs: 10, MaxMs: 20}) {
		t.Errorf("thinkTime = %+v, want {10 20}", dec.ThinkTime)
	}
}

// TestShardSpecValidateRejectsBadDeviationAndThink keeps a malformed shipped
// policy from silently running skewed on a worker.
func TestShardSpecValidateRejectsBadDeviationAndThink(t *testing.T) {
	t.Parallel()

	bad := linearSpec("http://t")
	bad.DeviationRate = 1.5
	if err := bad.Validate(); err == nil {
		t.Error("deviationRate 1.5: expected validation error, got nil")
	}

	bad = linearSpec("http://t")
	bad.ThinkTime = domain.ThinkTime{MinMs: 5, MaxMs: 1}
	if err := bad.Validate(); err == nil {
		t.Error("thinkTime 5..1: expected validation error, got nil")
	}
}

// TestWorkerAppliesDeviationFromSpec proves the rate reaches the walk on the
// worker: at rate 1.0 every step deviates, and the engine abandons roughly half
// of those deviations, so across many users strictly fewer than users*nodes
// requests reach the SUT (the run is seeded, so the count is deterministic).
func TestWorkerAppliesDeviationFromSpec(t *testing.T) {
	t.Parallel()

	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sut.Close)

	const users = 40
	conn := startWorker(t, WithAdapter(load.NewRESTAdapter(5*time.Second)))
	coord, err := NewCoordinator(conn)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	spec := linearSpec(sut.URL)
	spec.DeviationRate = 1.0

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stats, _, err := coord.Distribute(ctx, spec, users)
	if err != nil {
		t.Fatalf("distribute: %v", err)
	}

	// Every user requests the start node before any deviation decision, so the
	// floor is users; abandonment must keep the total under the full users*2.
	if stats.Total < users || stats.Total >= users*2 {
		t.Errorf("stats.Total = %d, want in [%d, %d) (deviation should abandon some journeys)", stats.Total, users, users*2)
	}
	if got := atomic.LoadInt64(&hits); got != int64(stats.Total) {
		t.Errorf("SUT hits = %d, want %d (every recorded step is a real request)", got, stats.Total)
	}
}

// TestWorkerAppliesThinkTimeFromSpec proves the shipped think time paces the
// worker's closed run: one user over a two-request chain must pause at least
// MinMs between its requests, so the shard takes at least that long.
func TestWorkerAppliesThinkTimeFromSpec(t *testing.T) {
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

	spec := linearSpec(sut.URL)
	spec.ThinkTime = domain.ThinkTime{MinMs: 40, MaxMs: 40}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	stats, _, err := coord.Distribute(ctx, spec, 1)
	if err != nil {
		t.Fatalf("distribute: %v", err)
	}
	if stats.Total != 2 {
		t.Fatalf("stats.Total = %d, want 2 (both chain nodes must still fire)", stats.Total)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Errorf("shard finished in %v, want >= 40ms (one think pause between the two requests)", elapsed)
	}
}
