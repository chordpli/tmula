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

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/obs"
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
	// Auth, when non-nil, supplies a credential per session keyed by the session's
	// global arrival index, so each open-model arrival authenticates as a distinct
	// principal (a pool wraps around its entries). Nil leaves every session
	// unauthenticated (the User's credential as-is), exactly as before.
	Auth auth.Provider
	// Seed makes both the arrival process and per-session graph traversal
	// reproducible: the sampler and the i-th session derive their RNG from it.
	Seed int64
	// RunID labels the findings produced from this run.
	RunID domain.ID
	// Classify tunes how observations become findings. It mirrors the closed
	// path's configuration so findings are comparable across models.
	Classify obs.ClassifyConfig
	// Collector, when non-nil, is the sink every request is recorded into, so a
	// caller (the control plane) can snapshot live stats while the run is still
	// in flight. When nil the scheduler uses a private collector and the stats
	// are only observable through the returned Result. Either way it must be
	// safe for concurrent use; obs.Collector is.
	Collector *obs.Collector
	// Segments, when non-empty, is the persona mix the arrivals are drawn from:
	// each arrival is assigned a segment in proportion to its weight and adopts
	// that segment's start node, step bound and think-time overrides. Empty means
	// one homogeneous persona using the Start/MaxSteps/Model.ThinkTime above.
	Segments []domain.Segment
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
	var picker *segmentPicker
	if len(opts.Segments) > 0 {
		if err := domain.ValidateSegments(opts.Segments); err != nil {
			return Result{}, fmt.Errorf("workload: invalid segments: %w", err)
		}
		picker = newSegmentPicker(opts.Segments)
	}

	rate, err := NewRateFunc(opts.Model.Arrival)
	if err != nil {
		return Result{}, err
	}
	lambdaMax := peakRate(opts.Model.Arrival)
	duration := time.Duration(opts.Model.DurationSeconds) * time.Second

	// Record into the caller's collector when provided so live stats are visible
	// mid-run; otherwise keep a private one. The final Result snapshots whichever
	// collector was used, so the returned stats are identical regardless.
	collector := opts.Collector
	if collector == nil {
		collector = obs.NewCollector()
	}
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
		// arrival is this session's global 1-based index. It seeds the session
		// (so traversal stays reproducible) and, when an auth provider is set,
		// keys the per-session credential so each arrival is a distinct principal.
		arrival := atomic.AddInt64(&idx, 1)
		sessionSeed := opts.Seed + arrival

		// Per-session profile: start from the run defaults, then let the segment
		// (persona) this arrival is drawn from override entry node, step bound and
		// think time. The segment is picked from the same loop-local rng, so the
		// mix is reproducible for a given seed.
		startNode := opts.Start
		maxSteps := opts.MaxSteps
		think := thinkFunc(opts.Model.ThinkTime, sessionSeed)
		segName := ""
		if picker != nil {
			seg := picker.pick(rng)
			segName = seg.Name
			if seg.Start != "" {
				startNode = seg.Start
			}
			if seg.MaxSteps > 0 {
				maxSteps = seg.MaxSteps
			}
			if seg.ThinkTime != nil {
				think = thinkFunc(*seg.ThinkTime, sessionSeed)
			}
		}
		user := opts.User
		user.ID = sessionUserID(opts.User.ID, segName, sessionSeed)
		// Per-session credential: key it by the arrival index so each session
		// authenticates as a distinct principal (a pool wraps around its entries).
		// A failed acquire is treated as a setup error — the same all-or-nothing
		// signal an unknown template raises — rather than silently running
		// unauthenticated. The pool provider's Acquire is pure, so this stays
		// deterministic. Use the zero index for the wrap math (arrival is 1-based).
		if opts.Auth != nil {
			cred, err := opts.Auth.Acquire(ctx, int(arrival-1))
			if err != nil {
				atomic.AddInt64(&setupErr, 1)
				setupOnce.Do(func() { firstSetupErr = err })
				atomic.AddInt64(&live, -1)
				continue
			}
			user.Cred = cred
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer atomic.AddInt64(&live, -1)
			results, err := s.runner.RunSession(ctx, opts.Graph, startNode, maxSteps, user, sessionSeed, think)
			if err != nil {
				// The session could not start (e.g. the graph references an unknown
				// API template). Setup is deterministic, so this fails every session
				// identically; count it and keep the first error so Run can fail
				// loudly instead of reporting an empty run as healthy.
				atomic.AddInt64(&setupErr, 1)
				setupOnce.Do(func() { firstSetupErr = err })
				return
			}
			record(collector, agg, results, segName, opts.Seed, s.clock.Now)
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
// mutation findings match across the open and closed models. Each observation is
// stamped via now() at the moment it is recorded, so availability now depends on
// per-request timestamp order (stable on ties), consistent with the closed path,
// rather than the order sessions happen to complete under concurrency.
//
// Each observation also carries the session's evidence context: its id, the
// persona it was drawn from, the reproduce coordinates (the seed the runner
// stamped on the result, and its offset from runSeed — the arrival number,
// since open sessions are seeded runSeed+arrival), and the failure path the
// runner attached to failed steps.
func record(collector *obs.Collector, agg *obs.Aggregator, results []load.StepResult, persona string, runSeed int64, now func() time.Time) {
	for _, sr := range results {
		cls := errorClass(sr)
		collector.Record(sr.Resp.StatusCode, sr.Resp.LatencyMs, cls)
		agg.Add(obs.RequestObservation{
			APIID:      sr.NodeID,
			StatusCode: sr.Resp.StatusCode,
			LatencyMs:  sr.Resp.LatencyMs,
			ErrorClass: cls,
			TS:         now(),
			SessionID:  sr.UserID,
			Seed:       sr.Seed,
			UserIndex:  sr.Seed - runSeed,
			Persona:    persona,
			Path:       sr.Path,
		})
	}
}

// errorClass maps a step result to the error class obs expects: empty for
// success, obs.TimeoutClass when the transport deadline elapsed (or the run was
// cancelled mid-request), and a generic "transport" class for any other send
// failure — the same classification the closed runner paths use.
func errorClass(sr load.StepResult) string {
	// A class the runtime stamped (e.g. obs.ErrorClassAuthRefresh on an
	// exhausted-refresh 401) wins, so the open path excuses the same auth churn the
	// closed path does. An exhausted-refresh 401 carries no Err, so this is the only
	// place its class survives.
	if sr.ErrorClass != "" {
		return sr.ErrorClass
	}
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
// id, the segment (persona) it was drawn from, and the session seed, so
// concurrent sessions stay distinguishable and a session's persona is visible in
// observations. The segment is omitted when the run has no persona mix.
func sessionUserID(base, segment string, seed int64) string {
	if base == "" {
		base = "vu"
	}
	if segment != "" {
		return fmt.Sprintf("%s-%s-s%d", base, segment, seed)
	}
	return fmt.Sprintf("%s-s%d", base, seed)
}

// segmentPicker draws a persona for each arrival in proportion to its weight,
// using inverse-CDF sampling over the cumulative weights. It holds no state
// beyond the precomputed table, so callers serialize draws through their own rng.
type segmentPicker struct {
	segs  []domain.Segment
	cum   []float64 // cumulative weights; cum[i] = sum(weights[:i+1])
	total float64
}

// newSegmentPicker precomputes the cumulative-weight table. Callers must pass a
// non-empty, validated slice (weights > 0).
func newSegmentPicker(segs []domain.Segment) *segmentPicker {
	p := &segmentPicker{segs: segs, cum: make([]float64, len(segs))}
	var sum float64
	for i, s := range segs {
		sum += s.Weight
		p.cum[i] = sum
	}
	p.total = sum
	return p
}

// pick returns a segment with probability weight/total. A draw in [0,total)
// lands in the first cumulative bucket it does not exceed.
func (p *segmentPicker) pick(rng *rand.Rand) domain.Segment {
	x := rng.Float64() * p.total
	for i, c := range p.cum {
		if x < c {
			return p.segs[i]
		}
	}
	return p.segs[len(p.segs)-1] // float rounding guard: total is the last bucket
}
