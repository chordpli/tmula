package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// runReproduce implements `tmula reproduce`: replay one evidence session of a
// finished run's finding in isolation (single session, no concurrent load) and
// classify the root cause from how often the failure recurred — functional
// (every attempt), load-dependent (none) or flaky (some). The replay happens
// on the engine that ran the experiment, because the seed coordinates only
// mean something next to the run's spec, which the engine holds in memory.
//
// The verdict is a signal, not a proof: the replay recreates the session's
// traffic composition (same seed, same walk), never the original timing or
// concurrency, and a target whose state changed since the run may answer
// differently. The engine's note restating this is printed with every result.
func runReproduce(args []string) error {
	fs := flag.NewFlagSet("tmula reproduce", flag.ContinueOnError)
	var (
		engine   = fs.String("engine", "", "engine base URL that ran (and still holds) the run (required)")
		runID    = fs.String("run", "", "run id whose finding to reproduce (required)")
		finding  = fs.String("finding", "", "which finding: category/evidenceRef (e.g. contract/checkout), or its 1-based index in the run's findings list (required)")
		attempts = fs.Int("attempts", 3, "how many isolated replays to run")
		asJSON   = fs.Bool("json", false, "print the raw reproduce JSON instead of the table")
		timeout  = fs.Duration("timeout", 2*time.Minute, "max time to wait for the replays")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: tmula reproduce --engine <url> --run <run-id> --finding <category/ref | index> [flags]\n\n"+
			"  tmula reproduce --engine http://localhost:8080 --run run-12 --finding contract/checkout\n"+
			"  tmula reproduce --engine http://localhost:8080 --run run-12 --finding 1 --attempts 5\n\n"+
			"Replays the finding's first evidence session alone (no load) to tell a\n"+
			"functional bug from a load-dependent one. The verdict is a signal, not a\n"+
			"proof — the replay cannot recreate the original timing or target state.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *engine == "" || *runID == "" || *finding == "" {
		return fmt.Errorf("--engine, --run and --finding are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cat, ref, err := resolveFindingSelector(ctx, *engine, *runID, *finding)
	if err != nil {
		return err
	}

	var res cliReproduce
	body := map[string]any{"category": cat, "evidenceRef": ref, "attempts": *attempts}
	if err := postJSON(ctx, *engine+"/api/runs/"+*runID+"/reproduce", body, &res); err != nil {
		return fmt.Errorf("reproduce: %w", err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	printReproduce(res)
	return nil
}

// resolveFindingSelector turns the --finding value into the (category,
// evidenceRef) key the engine replays by. It accepts the key directly as
// "category/evidenceRef" — the same textual form the known-issues file and the
// baseline gate print — or a 1-based index into the run's findings list,
// matching the order `tmula run` prints them in (the report's order).
func resolveFindingSelector(ctx context.Context, engineBase, runID, sel string) (string, string, error) {
	if idx, err := strconv.Atoi(sel); err == nil {
		var rep cliReport
		if err := getJSON(ctx, engineBase+"/api/runs/"+runID+"/report", &rep); err != nil {
			return "", "", fmt.Errorf("fetch run %q: %w", runID, err)
		}
		if idx < 1 || idx > len(rep.Findings) {
			return "", "", fmt.Errorf("--finding %d out of range: run %s has %d finding(s)", idx, runID, len(rep.Findings))
		}
		f := rep.Findings[idx-1]
		return f.Category, f.EvidenceRef, nil
	}
	cat, ref, ok := strings.Cut(sel, "/")
	if !ok || cat == "" || ref == "" {
		return "", "", fmt.Errorf("--finding must be category/evidenceRef (e.g. contract/checkout) or a 1-based findings index, got %q", sel)
	}
	return cat, ref, nil
}

// cliReproduce mirrors the engine's reproduce result JSON, like cliReport: the
// CLI consumes the wire shape without importing the control-plane types. The
// session object and its id arrive under "vu" (the evidence bundle's
// masker-safe naming).
type cliReproduce struct {
	RunID       string `json:"runId"`
	Category    string `json:"category"`
	EvidenceRef string `json:"evidenceRef"`
	Session     struct {
		ID        string   `json:"vu"`
		Seed      int64    `json:"seed"`
		UserIndex int64    `json:"userIndex"`
		Persona   string   `json:"persona"`
		Path      []string `json:"path"`
	} `json:"vu"`
	RunSeed        int64                 `json:"runSeed"`
	Attempts       []cliReproduceAttempt `json:"attempts"`
	Reproduced     int                   `json:"reproduced"`
	RootCauseClass string                `json:"rootCauseClass"`
	Note           string                `json:"note"`
}

// cliReproduceAttempt is one isolated replay as the CLI consumes it.
type cliReproduceAttempt struct {
	Reproduced bool               `json:"reproduced"`
	Steps      []cliReproduceStep `json:"steps"`
}

// cliReproduceStep is one request of a replayed session as the CLI consumes it.
type cliReproduceStep struct {
	Node       string  `json:"node"`
	StatusCode int     `json:"statusCode"`
	LatencyMs  float64 `json:"latencyMs"`
	ErrorClass string  `json:"errorClass"`
	Matched    bool    `json:"matched"`
}

// printReproduce renders the human-readable reproduce table: the session and
// its seed coordinates, the original failure path, every attempt's per-step
// status codes, and the verdict with its limits.
func printReproduce(r cliReproduce) {
	fmt.Printf("Reproduce %s/%s — run %s\n", r.Category, r.EvidenceRef, r.RunID)
	persona := ""
	if r.Session.Persona != "" {
		persona = "  persona=" + r.Session.Persona
	}
	fmt.Printf("  session %s  seed=%d (run seed %d + user index %d)%s\n",
		r.Session.ID, r.Session.Seed, r.RunSeed, r.Session.UserIndex, persona)
	if len(r.Session.Path) > 0 {
		fmt.Printf("  original failure path: %s\n", strings.Join(r.Session.Path, " → "))
	}

	fmt.Printf("\nAttempts (%d, single session, no concurrent load):\n", len(r.Attempts))
	for i, at := range r.Attempts {
		mark := "not reproduced"
		if at.Reproduced {
			mark = "REPRODUCED"
		}
		fmt.Printf("  #%d  %-14s  %s\n", i+1, mark, formatReproSteps(at.Steps))
	}

	fmt.Printf("\nVerdict: %s — %s\n", r.RootCauseClass, verdictPhrase(r.RootCauseClass, r.Reproduced, len(r.Attempts)))
	if r.Note != "" {
		fmt.Printf("Note: %s\n", r.Note)
	}
}

// formatReproSteps renders one attempt's steps as "node:status(latency)"
// entries, with the step(s) that carried the finding's signal marked by "!".
// Transport-level failures have no status code; the error class stands in.
func formatReproSteps(steps []cliReproduceStep) string {
	parts := make([]string, 0, len(steps))
	for _, st := range steps {
		code := strconv.Itoa(st.StatusCode)
		if st.StatusCode == 0 {
			code = st.ErrorClass
			if code == "" {
				code = "-"
			}
		}
		p := fmt.Sprintf("%s:%s(%.0fms)", st.Node, code, st.LatencyMs)
		if st.Matched {
			p += "!"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

// verdictPhrase spells the verdict out with its tally, phrased as likelihood —
// never certainty — because the replay cannot recreate the original timing.
func verdictPhrase(class string, reproduced, attempts int) string {
	switch class {
	case "functional":
		return fmt.Sprintf("reproduced %d/%d attempts under no load → likely a functional bug (independent of load)", reproduced, attempts)
	case "load-dependent":
		return fmt.Sprintf("reproduced %d/%d attempts under no load → likely load-dependent (concurrency or saturation)", reproduced, attempts)
	default:
		return fmt.Sprintf("reproduced %d/%d attempts under no load → flaky/uncertain; rerun with more --attempts or inspect the target", reproduced, attempts)
	}
}
