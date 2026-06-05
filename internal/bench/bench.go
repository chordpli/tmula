// Package bench is a capacity-measurement harness. It drives the load engine at
// a target concurrency against a system under test and measures the ACHIEVED
// throughput plus how closely that throughput tracks the issued-request
// expectation, so capacity goals (e.g. "local ~2,000 concurrent") can be
// validated empirically rather than assumed.
//
// It is a thin orchestration layer over the existing engine: it reuses
// load.NewRunner + load.RESTAdapter to generate traffic and obs.Collector to
// aggregate latency and error rate. The harness owns only the timing of the run
// and the derived capacity metrics (achieved RPS, tracking error).
package bench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/obs"
)

// Options configures a single capacity run.
type Options struct {
	// BaseURL is the system-under-test root the rendered requests target.
	BaseURL string
	// Graph is the behavior frame every virtual user traverses.
	Graph domain.ScenarioGraph
	// Templates maps APITemplate ID to its callable definition. A node with an
	// empty or unknown APITemplateID is treated as a pure state (no request).
	Templates map[domain.ID]domain.APITemplate
	// Start is the node every virtual user begins from.
	Start domain.ID
	// Users is the target concurrency: the number of virtual users driven
	// simultaneously, each as its own goroutine.
	Users int
	// MaxSteps bounds how many transitions a single user takes through the graph.
	MaxSteps int
	// Timeout is the per-request transport timeout handed to the REST adapter.
	Timeout time.Duration
	// Seed makes graph traversal reproducible; the i-th user walks with Seed+i.
	// Two runs with identical Options and a deterministic SUT issue the same
	// number of requests.
	Seed int64
}

// Result is the measured outcome of a capacity run.
type Result struct {
	// TargetConcurrency echoes Options.Users: the concurrency the run aimed for.
	TargetConcurrency int `json:"targetConcurrency"`
	// AchievedRPS is the realized throughput: TotalRequests / wall-clock seconds.
	AchievedRPS float64 `json:"achievedRps"`
	// TotalRequests is the number of requests actually issued to the SUT.
	TotalRequests int `json:"totalRequests"`
	// DurationMs is the wall-clock duration of the run in milliseconds.
	DurationMs float64 `json:"durationMs"`
	// ErrorRate is the fraction of requests that failed (0..1), from the
	// collector: status >= 400 or a transport error.
	ErrorRate float64 `json:"errorRate"`
	// TrackingErrorPct is how far the issued request count drifted from the
	// expected count (Users * requests-per-user), as a percentage. Zero means
	// every user issued exactly the expected number of requests; see
	// TrackingErrorPct for the precise definition.
	TrackingErrorPct float64 `json:"trackingErrorPct"`
	// P50, P95, P99 are client-side latency percentiles in milliseconds, taken
	// from obs.Collector over every request in the run.
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

// Run drives the engine at Options.Users concurrency, times the run, feeds every
// step result into an obs.Collector, and returns the derived capacity metrics.
//
// Per-request failures are recorded (they shape ErrorRate and the latency
// percentiles) rather than aborting the run; only a setup failure (e.g. a node
// referencing an unknown template) returns a non-nil error. The run honors ctx:
// cancelling it stops every user's journey promptly, and the partial result is
// still measured.
func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Users <= 0 {
		return Result{}, fmt.Errorf("bench: Users must be > 0, got %d", opts.Users)
	}
	if opts.Timeout <= 0 {
		return Result{}, fmt.Errorf("bench: Timeout must be > 0, got %v", opts.Timeout)
	}

	users := make([]load.VirtualUser, opts.Users)
	for i := range users {
		users[i] = load.VirtualUser{ID: fmt.Sprintf("vu-%d", i)}
	}

	adapter := load.NewRESTAdapter(opts.Timeout)
	runner := load.NewRunner(adapter, opts.BaseURL, opts.Templates)

	start := time.Now()
	results, err := runner.Run(ctx, opts.Graph, opts.Start, opts.MaxSteps, users, opts.Seed)
	elapsed := time.Since(start)
	if err != nil {
		return Result{}, fmt.Errorf("bench: run: %w", err)
	}

	collector := obs.NewCollector()
	for _, sr := range results {
		collector.Record(sr.Resp.StatusCode, sr.Resp.LatencyMs, errorClass(sr))
	}
	stats := collector.Snapshot()

	durationMs := float64(elapsed.Microseconds()) / 1000.0
	res := Result{
		TargetConcurrency: opts.Users,
		TotalRequests:     stats.Total,
		DurationMs:        durationMs,
		ErrorRate:         stats.ErrorRate,
		P50:               stats.P50,
		P95:               stats.P95,
		P99:               stats.P99,
	}
	if seconds := elapsed.Seconds(); seconds > 0 {
		res.AchievedRPS = float64(stats.Total) / seconds
	}
	res.TrackingErrorPct = TrackingErrorPct(stats.Total, expectedRequests(opts, results))

	return res, nil
}

// errorClass maps a step result to the error class obs.Collector expects: empty
// for success, obs.TimeoutClass when the transport deadline elapsed, and a
// generic "transport" class for any other send failure. The collector counts
// any non-empty class as an error, so the exact string only distinguishes
// timeouts from other failures.
func errorClass(sr load.StepResult) string {
	if sr.Err == nil {
		return ""
	}
	if errors.Is(sr.Err, context.DeadlineExceeded) || errors.Is(sr.Err, os.ErrDeadlineExceeded) {
		return obs.TimeoutClass
	}
	return "transport"
}

// expectedRequests derives how many requests the run was expected to issue: the
// number of distinct users observed times the per-user request count. Deriving
// the per-user count from the realized path (rather than assuming graph size)
// keeps the expectation honest for graphs whose traversal length varies, while
// still flagging users who issued an off-by-N number of requests.
func expectedRequests(opts Options, results []load.StepResult) int {
	perUser := make(map[string]int, opts.Users)
	for _, sr := range results {
		perUser[sr.UserID]++
	}
	if len(perUser) == 0 {
		return 0
	}
	// Use the modal per-user request count as the expectation: the count the
	// most users agreed on. Drift from it is what TrackingErrorPct reports.
	mode, modeFreq := 0, 0
	freq := make(map[int]int, len(perUser))
	for _, n := range perUser {
		freq[n]++
		if freq[n] > modeFreq || (freq[n] == modeFreq && n > mode) {
			mode, modeFreq = n, freq[n]
		}
	}
	return mode * len(perUser)
}

// TrackingErrorPct reports how closely the achieved request count tracked the
// expected count, as a percentage: |achieved - expected| / expected * 100.
//
// It answers "did the engine issue the load we asked it to?" — the capacity
// analogue of a control system's tracking error. 0 means the engine issued
// exactly the expected number of requests (perfect tracking); a larger value
// means requests were dropped or duplicated relative to expectation. When
// expected is 0 the metric is defined as 0 if achieved is also 0 (nothing was
// expected and nothing happened) and 100 otherwise (unexpected requests with no
// baseline to track against), avoiding a divide-by-zero.
func TrackingErrorPct(achieved, expected int) float64 {
	if expected == 0 {
		if achieved == 0 {
			return 0
		}
		return 100
	}
	diff := achieved - expected
	if diff < 0 {
		diff = -diff
	}
	return float64(diff) / float64(expected) * 100
}
