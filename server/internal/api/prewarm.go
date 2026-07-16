package api

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// prewarmAcquire provisions/authenticates the credential for one index. It is the
// per-index unit prewarmBounded fans out (a bootstrap signup, a login mint).
type prewarmAcquire func(ctx context.Context, index int) error

// prewarmBounded provisions indices [0,n) ahead of the run with at most
// `concurrency` in flight, so the burst is parallel yet never wider than the
// caller's bound (the run's rate-cap concurrency) — the prewarm must not itself
// load-test the IdP / signup endpoint. The first error aborts (a failed
// provision must fail the run, not run it half-authenticated) and cancels the
// rest of the burst; a canceled context stops scheduling. A concurrency <= 0 is
// floored at 1 so a zero/negative cap still makes progress (sequentially).
//
// It is the shared spine both the bootstrap-signup and login strategies prewarm
// through, so the two behave identically under a rate cap.
func prewarmBounded(ctx context.Context, n, concurrency int, acquire prewarmAcquire) error {
	if n <= 0 {
		return nil
	}
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var (
		wg        sync.WaitGroup
		errOnce   sync.Once
		firstErr  error
		completed int64
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Progress cadence: log roughly every 10% of the burst (never per-credential),
	// so a large prewarm (hundreds of thousands of accounts) shows it is making
	// headway instead of looking hung. A small prewarm (< progressDecile) logs no
	// progress — it completes near-instantly. The log carries only COUNTS and a rate,
	// never a token value.
	step := int64(n) / prewarmProgressDeciles
	start := time.Now()
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			i = n // stop scheduling
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := acquire(ctx, idx); err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel() // stop the rest of the burst on the first failure
				})
				return
			}
			logPrewarmProgress(atomic.AddInt64(&completed, 1), int64(n), step, start)
		}(i)
	}
	wg.Wait()
	return firstErr
}

// prewarmProgressDeciles sets the progress cadence: a burst is logged at each ~1/10th
// of its total (plus completion). A prewarm smaller than this logs no progress.
const prewarmProgressDeciles = 10

// logPrewarmProgress emits a single slog.Info line at each ~10% boundary of a prewarm
// burst (and at completion), carrying the completed count, the total, and the effective
// acquisition rate. It NEVER logs a token or credential value — only counts and a rate —
// so the progress trail is safe. A step of 0 (a burst too small to have deciles) logs
// nothing.
func logPrewarmProgress(done, total, step int64, start time.Time) {
	if step < 1 {
		return
	}
	if done%step != 0 && done != total {
		return
	}
	rate := 0.0
	if elapsed := time.Since(start).Seconds(); elapsed > 0 {
		rate = float64(done) / elapsed
	}
	slog.Info("auth prewarm progress", "acquired", done, "total", total, "perSec", int(rate))
}

// prewarmConcurrencyFor resolves the prewarm burst width from a rate cap:
// min(RateCap.MaxConcurrency, hardCap), floored at 1. The hardCap is the
// strategy-specific ceiling (bootstrap's provisioning cap, login's IdP-protection
// cap) so a huge rate cap cannot turn a prewarm into an accidental IdP flood.
func prewarmConcurrencyFor(rateCapMaxConcurrency, hardCap int) int {
	c := rateCapMaxConcurrency
	if c > hardCap {
		c = hardCap
	}
	if c < 1 {
		c = 1
	}
	return c
}
