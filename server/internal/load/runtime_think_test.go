package load

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// virtualClock stands in for the runner's think pause: instead of blocking, each
// requested pause advances a shared virtual time, so a test asserts pacing
// without ever waiting in real time.
type virtualClock struct {
	mu     sync.Mutex
	now    time.Duration
	pauses int
}

// sleep is the injected pacer: it advances virtual time by d and never blocks.
func (c *virtualClock) sleep(_ context.Context, d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now += d
	c.pauses++
	return true
}

func (c *virtualClock) read() (time.Duration, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now, c.pauses
}

// stampingAdapter records the virtual time at which each request fires, so the
// inter-request gaps the think time should produce are directly observable.
type stampingAdapter struct {
	clock  *virtualClock
	mu     sync.Mutex
	stamps []time.Duration
}

func (a *stampingAdapter) Protocol() domain.Protocol { return domain.ProtocolREST }

func (a *stampingAdapter) Send(_ context.Context, _ RenderedRequest) (Response, error) {
	now, _ := a.clock.read()
	a.mu.Lock()
	a.stamps = append(a.stamps, now)
	a.mu.Unlock()
	return Response{StatusCode: 200, LatencyMs: 1}, nil
}

// TestClosedRunThinkTimePacesRequests unblocks the closed model's think time: a
// Run configured with a WorkloadModel think range pauses each user between
// consecutive requests, observable here as virtual-clock gaps — the closed pool
// stops hammering with zero pause once a think time is configured.
func TestClosedRunThinkTimePacesRequests(t *testing.T) {
	g, tmpls := chainGraph(3) // a -> b -> c, one request per node
	clock := &virtualClock{}
	adapter := &stampingAdapter{clock: clock}
	r := NewRunner(adapter, "http://t", tmpls,
		WithThinkTime(domain.ThinkTime{MinMs: 100, MaxMs: 100}),
		withSleep(clock.sleep),
	)

	if _, err := r.Run(context.Background(), g, "a", 5, []VirtualUser{{ID: "u"}}, 1); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := []time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond}
	if !slices.Equal(adapter.stamps, want) {
		t.Errorf("request stamps = %v, want %v (a 100ms pause before every request after the first)", adapter.stamps, want)
	}
	if _, pauses := clock.read(); pauses != 2 {
		t.Errorf("pauses = %d, want 2 (between a-b and b-c, never before the first request)", pauses)
	}
}

// TestClosedRunNoThinkTimeNeverPauses pins the default: with no think time
// configured (or a zero range) the closed Run never sleeps, so every existing
// closed-model caller and test keeps its zero-pause timing.
func TestClosedRunNoThinkTimeNeverPauses(t *testing.T) {
	g, tmpls := chainGraph(3)

	for _, tc := range []struct {
		name string
		opt  []RunnerOption
	}{
		{"unset", nil},
		{"zero value", []RunnerOption{WithThinkTime(domain.ThinkTime{})}},
	} {
		clock := &virtualClock{}
		adapter := &stampingAdapter{clock: clock}
		opts := append(tc.opt, withSleep(clock.sleep))
		r := NewRunner(adapter, "http://t", tmpls, opts...)
		if _, err := r.Run(context.Background(), g, "a", 5, []VirtualUser{{ID: "u"}}, 1); err != nil {
			t.Fatalf("%s: run: %v", tc.name, err)
		}
		if now, pauses := clock.read(); pauses != 0 || now != 0 {
			t.Errorf("%s: pauses=%d virtual=%v, want no pause at all", tc.name, pauses, now)
		}
	}
}

// TestClosedRunThinkTimeIsSeeded pins reproducibility: a variable think range
// draws from a per-user RNG derived from the user's walk seed, so two identical
// runs pause identically.
func TestClosedRunThinkTimeIsSeeded(t *testing.T) {
	g, tmpls := chainGraph(3)

	run := func() []time.Duration {
		clock := &virtualClock{}
		adapter := &stampingAdapter{clock: clock}
		r := NewRunner(adapter, "http://t", tmpls,
			WithThinkTime(domain.ThinkTime{MinMs: 50, MaxMs: 150}),
			withSleep(clock.sleep),
		)
		if _, err := r.Run(context.Background(), g, "a", 5, []VirtualUser{{ID: "u"}}, 7); err != nil {
			t.Fatalf("run: %v", err)
		}
		return adapter.stamps
	}

	first, second := run(), run()
	if !slices.Equal(first, second) {
		t.Errorf("think pacing not reproducible for a fixed seed: %v vs %v", first, second)
	}
	// The configured range must actually pause: the last request fires at least
	// 2*MinMs into the session (two gaps of >= 50ms each).
	if last := first[len(first)-1]; last < 100*time.Millisecond {
		t.Errorf("last request at %v, want >= 100ms (two pauses of >= MinMs)", last)
	}
}
