package workload

import (
	"context"
	"sync"
	"time"
)

// Clock is the scheduler's view of time, injectable so tests can drive arrival
// sampling deterministically without waiting in real time. Production uses a
// real clock; tests can substitute a virtual one whose Sleep returns at once but
// still advances Now, so a whole run's worth of arrivals is sampled instantly.
type Clock interface {
	// Now reports the current instant.
	Now() time.Time
	// Sleep blocks for at least d or until ctx is done, whichever comes first. It
	// reports true if the duration elapsed and false if ctx was cancelled. The
	// scheduler relies on the ctx-cancellation path for prompt shutdown.
	Sleep(ctx context.Context, d time.Duration) bool
}

// realClock is the production Clock backed by the standard time package.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// virtualClock is a deterministic Clock for tests: Sleep advances a logical
// "now" by the requested duration and returns immediately (honoring ctx), so the
// arrival sampler runs through an entire simulated run in real-time-zero while
// the launched sessions still execute against a real httptest server. It is safe
// for concurrent reads of Now from launched sessions while the scheduler advances
// it from the sampling loop.
type virtualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newVirtualClock(start time.Time) *virtualClock { return &virtualClock{now: start} }

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) Sleep(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d > 0 {
		c.mu.Lock()
		c.now = c.now.Add(d)
		c.mu.Unlock()
	}
	return true
}
