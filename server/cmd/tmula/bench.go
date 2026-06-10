package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/chordpli/tmula/server/internal/bench"
	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// runBench implements `tmula bench`: drive the bench harness at a target
// concurrency against a system under test and print the capacity metrics.
// It mirrors the flag layout of `tmula run` for the scenario / single-endpoint
// forms, but uses bench.Run directly instead of going through the control plane.
func runBench(args []string) error {
	fs := flag.NewFlagSet("tmula bench", flag.ContinueOnError)
	var (
		target   = fs.String("target", "", "target base URL (overrides the scenario file)")
		get      = fs.String("get", "", "single-endpoint mode: GET this path (no scenario file)")
		post     = fs.String("post", "", "single-endpoint mode: POST this path (no scenario file)")
		users    = fs.Int("users", 50, "target concurrency (virtual user count)")
		maxSteps = fs.Int("max-steps", 0, "max transitions per virtual user (default: flow length)")
		timeout  = fs.Duration("timeout", 10*time.Second, "per-request transport timeout")
		seed     = fs.Int64("seed", 0, "random seed for graph traversal (default 1)")
		asJSON   = fs.Bool("json", false, "print raw result JSON instead of a summary")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: tmula bench [scenario.yaml] [flags]\n\n"+
			"  tmula bench scenario.yaml --users 100\n"+
			"  tmula bench --target http://localhost:9000 --get /health --users 50\n\n")
		fs.PrintDefaults()
	}

	// Collect positional arguments the same way runScenario does: loop to allow
	// flags to appear before or after the scenario file.
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) > 1 {
		return fmt.Errorf("unexpected extra arguments: %v", positional[1:])
	}
	file := ""
	if len(positional) == 1 {
		file = positional[0]
	}

	sc, err := buildScenario(file, *target, *get, *post)
	if err != nil {
		return err
	}
	if sc.Target == "" {
		return fmt.Errorf("no target URL in the scenario; pass --target")
	}

	spec, err := scenariofile.Expand(sc)
	if err != nil {
		return err
	}

	ms := *maxSteps
	if ms <= 0 {
		ms = spec.MaxSteps
	}

	sd := *seed
	if sd == 0 {
		sd = spec.Seed
	}

	opts := bench.Options{
		BaseURL:   spec.TargetEnv.BaseURL,
		Graph:     spec.Graph,
		Templates: spec.Templates,
		Start:     spec.Start,
		Users:     *users,
		MaxSteps:  ms,
		Timeout:   *timeout,
		Seed:      sd,
	}

	ctx := context.Background()
	result, err := bench.Run(ctx, opts)
	if err != nil {
		return fmt.Errorf("bench: %w", err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	printBenchResult(result)
	return nil
}

// printBenchResult renders a human-readable capacity summary.
func printBenchResult(r bench.Result) {
	fmt.Printf("Bench — target concurrency: %d\n", r.TargetConcurrency)
	fmt.Printf("  achieved RPS:     %.1f\n", r.AchievedRPS)
	fmt.Printf("  total requests:   %d\n", r.TotalRequests)
	fmt.Printf("  duration:         %.0f ms\n", r.DurationMs)
	fmt.Printf("  error rate:       %.2f%%\n", r.ErrorRate*100)
	fmt.Printf("  tracking error:   %.2f%%\n", r.TrackingErrorPct)
	fmt.Printf("  latency p50/p95/p99: %.0f ms / %.0f ms / %.0f ms\n",
		r.P50, r.P95, r.P99)
	// bench.Run zero-guards AchievedRPS behind elapsed>0, so a run too short to
	// time reports RPS as 0 (never Inf/NaN) even though it made requests. That
	// (plus the defensive Inf/NaN guard) is the real "unmeasurable RPS" signal.
	if (r.TotalRequests > 0 && r.AchievedRPS == 0) || math.IsInf(r.AchievedRPS, 0) || math.IsNaN(r.AchievedRPS) {
		fmt.Println("\n  (run was too short to measure RPS accurately)")
	}
}
