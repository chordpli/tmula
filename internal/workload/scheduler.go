package workload

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/obs"
)

// Options configures one open-model run.
type Options struct {
	// Graph is the behavior frame every arriving user traverses.
	Graph domain.ScenarioGraph
	// Start is the node every session begins from.
	Start domain.ID
	// MaxSteps bounds how many transitions a single session takes.
	MaxSteps int
	// Model carries the arrival profile, run duration, back-pressure cap and
	// think time. Its Kind must be WorkloadOpen.
	Model domain.WorkloadModel
	// User is the identity each session runs as. Sessions are independent
	// arrivals of (conceptually) the same principal; the per-session index is
	// appended to the ID so observations remain distinguishable.
	User load.VirtualUser
	// Seed makes both the arrival process and per-session graph traversal
	// reproducible: the sampler and the i-th session derive their RNG from it.
	Seed int64
	// RunID labels the findings produced from this run.
	RunID domain.ID
	// Classify tunes how observations become findings. It mirrors the closed
	// path's configuration so findings are comparable across models.
	Classify obs.ClassifyConfig
}

// Result is the aggregated outcome of an open-model run.
type Result struct {
	// Stats is the client-side latency/error aggregate over every request issued.
	Stats obs.Stats
	// Findings are classified identically to the closed path (same categories).
	Findings []domain.Finding
	// Launched is the number of sessions that were admitted and run.
	Launched int
	// Dropped is the number of arrivals shed by back-pressure (the cap was full).
	// A non-zero value is the observable signal that demand exceeded capacity.
	Dropped int
	// SetupErrors is the number of admitted sessions that could not even start
	// (the graph references an unknown API template). Setup is deterministic and
	// identical for every session, so this is all-or-nothing: when it is non-zero
	// the graph is misconfigured and Run returns an error rather than reporting a
	// healthy-looking empty run.
	SetupErrors int
}

// Scheduler runs an open workload: it launches sessions over time as a Poisson
// process whose intensity tracks the arrival profile's rate(t), bounded by a
// back-pressure cap, reusing a load.Runner for each session's journey.
type Scheduler struct {
	runner *load.Runner
	clock  Clock
}

// New builds a Scheduler driving sessions through runner. By default it uses a
// real clock; pass WithClock to inject a deterministic one for tests.
func New(runner *load.Runner, opts ...Option) *Scheduler {
	s := &Scheduler{runner: runner, clock: realClock{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option customizes a Scheduler.
type Option func(*Scheduler)

// WithClock injects the clock the scheduler uses for arrival timing, so tests can
// advance time deterministically instead of waiting in real time.
func WithClock(c Clock) Option { return func(s *Scheduler) { s.clock = c } }

// Run executes the open workload described by opts and blocks until the arrival
// window closes (or ctx is cancelled), then until every in-flight session
// finishes. Every step is recorded into a shared collector and finding
// aggregator exactly as the closed path records them, so the rate, contract and
// mutation findings match the closed path for identical traffic. Availability is
// run-length based, so under concurrency it additionally depends on the order
// sessions complete. Cancelling ctx stops new arrivals promptly and lets running
// sessions unwind (their own ctx is cancelled too).
func (s *Scheduler) Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Model.Kind != domain.WorkloadOpen {
		return Result{}, fmt.Errorf("workload: scheduler requires open model, got %q", opts.Model.Kind)
	}
	if err := opts.Model.Validate(); err != nil {
		return Result{}, fmt.Errorf("workload: invalid model: %w", err)
	}

	rate, err := NewRateFunc(opts.Model.Arrival)
	if err != nil {
		return Result{}, err
	}
	lambdaMax := peakRate(opts.Model.Arrival)
	duration := time.Duration(opts.Model.DurationSeconds) * time.Second

	collector := obs.NewCollector()
	agg := obs.NewAggregator()

	var (
		wg       sync.WaitGroup
		live     int64 // sessions currently running (back-pressure gauge)
		launched int64
		dropped  int64
		setupErr int64 // sessions that failed to start (unknown template, etc.)
	)
	// firstSetupErr captures one representative setup failure so a misconfigured
	// run fails with an actionable message. Written once under setupOnce inside
	// session goroutines; read after wg.Wait, so the WaitGroup orders the access.
	var (
		setupOnce     sync.Once
		firstSetupErr error
	)
	capLimit := opts.Model.MaxConcurrency // 0 means uncapped

	// ctx governs both the arrival window and the launched sessions, so a kill
	// switch stops sampling and unwinds in-flight work through the same signal.
	rng := rand.New(rand.NewSource(opts.Seed))
	start := s.clock.Now()
	var idx int64

	for {
		if ctx.Err() != nil {
			break
		}
		// Thinning (Lewis–Shedler): advance by an exponential gap drawn at the
		// ceiling rate λmax, then keep this candidate as a real arrival with
		// probability rate(t)/λmax. The kept arrivals form a Poisson process whose
		// instantaneous intensity is exactly rate(t) — organic, not clockwork, and
		// faithful to ramps/spikes without a fixed tick.
		gap := expGap(rng, lambdaMax)
		if !s.clock.Sleep(ctx, gap) {
			break // ctx cancelled during the inter-arrival wait
		}
		elapsed := s.clock.Now().Sub(start)
		if elapsed >= duration {
			break // arrival window closed
		}
		r := rate(elapsed)
		if r <= 0 {
			continue // quiet period (e.g. soak after hold): no arrivals
		}
		if r < lambdaMax && rng.Float64() >= r/lambdaMax {
			continue // thinned out: this candidate is not a real arrival
		}

		// Back-pressure: never exceed the cap. When full, the arrival is dropped
		// and counted so the shortfall is observable, rather than queued (queuing
		// would silently distort the offered load).
		if capLimit > 0 && atomic.LoadInt64(&live) >= int64(capLimit) {
			atomic.AddInt64(&dropped, 1)
			continue
		}

		atomic.AddInt64(&live, 1)
		atomic.AddInt64(&launched, 1)
		sessionSeed := opts.Seed + atomic.AddInt64(&idx, 1)
		user := opts.User
		user.ID = sessionUserID(opts.User.ID, sessionSeed)
		think := thinkFunc(opts.Model.ThinkTime, sessionSeed)

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer atomic.AddInt64(&live, -1)
			results, err := s.runner.RunSession(ctx, opts.Graph, opts.Start, opts.MaxSteps, user, sessionSeed, think)
			if err != nil {
				// The session could not start (e.g. the graph references an unknown
				// API template). Setup is deterministic, so this fails every session
				// identically; count it and keep the first error so Run can fail
				// loudly instead of reporting an empty run as healthy.
				atomic.AddInt64(&setupErr, 1)
				setupOnce.Do(func() { firstSetupErr = err })
				return
			}
			record(collector, agg, results, s.clock.Now())
		}()
	}

	wg.Wait()

	res := Result{
		Stats:       collector.Snapshot(),
		Findings:    agg.Classify(opts.RunID, opts.Classify),
		Launched:    int(atomic.LoadInt64(&launched)),
		Dropped:     int(atomic.LoadInt64(&dropped)),
		SetupErrors: int(atomic.LoadInt64(&setupErr)),
	}
	// Sessions were admitted but not one produced an observation because they all
	// failed to start: the graph is misconfigured. Surface it as an error so the
	// run is reported as failed rather than as a healthy, empty success.
	if res.SetupErrors > 0 && res.Stats.Total == 0 {
		return res, fmt.Errorf("workload: every launched session failed to start: %w", firstSetupErr)
	}
	return res, nil
}

// expGap returns an exponential inter-arrival gap for rate lambda (per second),
// i.e. the time to the next event in a Poisson process of that intensity.
func expGap(rng *rand.Rand, lambda float64) time.Duration {
	if lambda <= 0 {
		return 0
	}
	// rng.Float64() is in [0,1); 1-u is in (0,1] so the log is finite.
	secs := -math.Log(1-rng.Float64()) / lambda
	return time.Duration(secs * float64(time.Second))
}

// thinkFunc builds the per-session think-time provider: a uniform draw in
// [MinMs, MaxMs] from a session-local RNG (seeded from sessionSeed so it is
// reproducible and never shared across goroutines). A zero range yields nil, so
// no pause is taken.
func thinkFunc(tt domain.ThinkTime, sessionSeed int64) load.ThinkFunc {
	if tt.MaxMs <= 0 {
		return nil
	}
	// Offset the seed so think time does not correlate with traversal choices,
	// which draw from a walker seeded by the same sessionSeed.
	rng := rand.New(rand.NewSource(sessionSeed ^ 0x5DEECE66D))
	span := tt.MaxMs - tt.MinMs
	return func() time.Duration {
		ms := tt.MinMs
		if span > 0 {
			ms += rng.Intn(span + 1)
		}
		return time.Duration(ms) * time.Millisecond
	}
}

// record feeds one session's step results into the shared collector and finding
// aggregator using the SAME mapping as the closed path (internal/api executeLocal):
// status/latency/class into the collector, and a RequestObservation keyed by the
// visited node id into the aggregator. This is what makes the rate, contract and
// mutation findings match across the open and closed models; availability, being
// run-length based, also depends on the order sessions complete under concurrency.
func record(collector *obs.Collector, agg *obs.Aggregator, results []load.StepResult, ts time.Time) {
	for _, sr := range results {
		cls := errorClass(sr)
		collector.Record(sr.Resp.StatusCode, sr.Resp.LatencyMs, cls)
		agg.Add(obs.RequestObservation{
			APIID:      sr.NodeID,
			StatusCode: sr.Resp.StatusCode,
			LatencyMs:  sr.Resp.LatencyMs,
			ErrorClass: cls,
			TS:         ts,
		})
	}
}

// errorClass maps a step result to the error class obs expects: empty for
// success, obs.TimeoutClass when the transport deadline elapsed (or the run was
// cancelled mid-request), and a generic "transport" class for any other send
// failure — the same classification the closed runner paths use.
func errorClass(sr load.StepResult) string {
	if sr.Err == nil {
		return ""
	}
	if errors.Is(sr.Err, context.DeadlineExceeded) ||
		errors.Is(sr.Err, context.Canceled) ||
		errors.Is(sr.Err, os.ErrDeadlineExceeded) {
		return obs.TimeoutClass
	}
	return "transport"
}

// sessionUserID derives a stable, unique id for one arrival from the base user
// id and the session seed, so concurrent sessions of the same principal remain
// distinguishable in observations.
func sessionUserID(base string, seed int64) string {
	if base == "" {
		base = "vu"
	}
	return fmt.Sprintf("%s-s%d", base, seed)
}
