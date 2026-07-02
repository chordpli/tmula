package api

import (
	"context"
	"sync"
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
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
			}
		}(i)
	}
	wg.Wait()
	return firstErr
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
