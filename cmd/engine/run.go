package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/chordpli/tmula/internal/api"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/scenariofile"
)

// errFindings signals that findings were detected under --fail-on-findings. main
// maps it to a quiet non-zero exit — an expected gate outcome, not a crash.
var errFindings = errors.New("findings detected")

// runScenario implements `tmula run`: build a RunSpec from a scenario file (or
// single-endpoint flags), execute it — in-process by default, or against a
// running engine via --engine — and print the findings.
func runScenario(args []string) error {
	fs := flag.NewFlagSet("tmula run", flag.ContinueOnError)
	var (
		target         = fs.String("target", "", "target base URL (overrides the scenario file)")
		get            = fs.String("get", "", "single-endpoint mode: GET this path (no scenario file)")
		post           = fs.String("post", "", "single-endpoint mode: POST this path (no scenario file)")
		users          = fs.Int("users", 0, "virtual user count for the closed model")
		openR          = fs.Float64("open", 0, "open model: arrivals per second")
		forSec         = fs.Int("for", 0, "open model: how long to keep arriving (seconds)")
		rampTo         = fs.Float64("ramp-to", 0, "open model: ramp peak rate (uses --open as the start)")
		seed           = fs.Int64("seed", 0, "random seed (default 1)")
		engine         = fs.String("engine", "", "run against an existing engine base URL instead of in-process")
		asJSON         = fs.Bool("json", false, "print the raw report JSON instead of a summary")
		failOnFindings = fs.Bool("fail-on-findings", false, "exit non-zero if any finding is detected (CI gate)")
		failOnSeverity = fs.String("fail-on-severity", "", "gate only on findings at/above this severity: warning | critical")
		timeout        = fs.Duration("timeout", 2*time.Minute, "max time to wait for the run to finish")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: tmula run [scenario.yaml] [flags]\n\n"+
			"  tmula run scenario.yaml --users 50\n"+
			"  tmula run --target http://localhost:9000 --get /health --users 20\n"+
			"  tmula run scenario.yaml --open 278 --for 3600\n\n"+
			"exit codes: 0 ok · 1 error · 2 findings (with --fail-on-findings)\n\n")
		fs.PrintDefaults()
	}
	// Go's flag package stops at the first non-flag argument, so a natural
	// invocation like `tmula run scenario.yaml --users 50` would drop the flags.
	// Loop to collect positionals and keep parsing the remainder, so flags may
	// appear before or after the scenario file.
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

	switch strings.ToLower(*failOnSeverity) {
	case "", "warning", "critical":
	default:
		return fmt.Errorf("--fail-on-severity must be warning or critical, got %q", *failOnSeverity)
	}

	sc, err := buildScenario(file, *target, *get, *post)
	if err != nil {
		return err
	}
	if *users > 0 {
		sc.Users = *users
	}
	if *seed != 0 {
		sc.Seed = *seed
	}
	if *openR > 0 || *forSec > 0 || *rampTo > 0 {
		if sc.Open == nil {
			sc.Open = &scenariofile.Open{}
		}
		switch {
		case *rampTo > 0:
			sc.Open.From, sc.Open.To = *openR, *rampTo
		case *openR > 0:
			sc.Open.Rate = *openR
		}
		if *forSec > 0 {
			sc.Open.ForSeconds = *forSec
		}
	}

	if sc.Target == "" {
		return fmt.Errorf("no target URL in the scenario; pass --target")
	}
	spec, err := scenariofile.Expand(sc)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	base := *engine
	if base == "" {
		stop, b, err := startInProcessEngine()
		if err != nil {
			return err
		}
		defer stop()
		base = b
	}

	report, err := driveRun(ctx, base, spec)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		printReport(report)
	}

	// A run that did not complete cleanly (failed or killed — e.g. a timeout or
	// circuit-breaker trip) is a non-zero exit regardless of findings, so it
	// never silently passes a CI gate. A "failed" run often has no kill reason, so
	// only append the reason when there is one (avoids a dangling "run failed: ").
	if s := report.Run.Status; s == "failed" || s == "killed" {
		if report.Run.KillReason != "" {
			return fmt.Errorf("run %s: %s", s, report.Run.KillReason)
		}
		return fmt.Errorf("run %s", s)
	}
	if n := gatingFindings(report.Findings, *failOnFindings, strings.ToLower(*failOnSeverity)); n > 0 {
		return fmt.Errorf("%w (%d)", errFindings, n)
	}
	return nil
}

// gatingFindings counts the findings that should fail the CI gate. The gate is
// off (returns 0) unless failAny is set or minSev is non-empty. minSev
// "warning" counts every finding (warning is the lowest level); "critical"
// counts only criticals.
func gatingFindings(findings []cliFinding, failAny bool, minSev string) int {
	if !failAny && minSev == "" {
		return 0
	}
	n := 0
	for _, f := range findings {
		if minSev == "critical" && strings.ToLower(f.Severity) != "critical" {
			continue
		}
		n++
	}
	return n
}

// buildScenario assembles the scenario from a file or, when none is given, from
// single-endpoint flags (--target with --get/--post). A --target always
// overrides the file's target.
func buildScenario(file, target, get, post string) (scenariofile.Scenario, error) {
	if file == "" {
		if get == "" && post == "" {
			return scenariofile.Scenario{}, fmt.Errorf("provide a scenario file, or --get/--post with --target")
		}
		if get != "" && post != "" {
			return scenariofile.Scenario{}, fmt.Errorf("use only one of --get or --post (single-endpoint mode is one request)")
		}
		if target == "" {
			return scenariofile.Scenario{}, fmt.Errorf("--target is required in single-endpoint mode")
		}
		req := "GET " + get
		if post != "" {
			req = "POST " + post
		}
		return scenariofile.Scenario{
			Target: target,
			Flow:   []scenariofile.Step{{ID: "step", Request: req}},
		}, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return scenariofile.Scenario{}, fmt.Errorf("read scenario %q: %w", file, err)
	}
	sc, err := scenariofile.Parse(data)
	if err != nil {
		return scenariofile.Scenario{}, err
	}
	if target != "" {
		sc.Target = target
	}
	return sc, nil
}

// startInProcessEngine boots a local control plane on an ephemeral loopback port
// so `tmula run` needs no separately running server. The returned stop func
// drains in-flight work and shuts the listener down.
func startInProcessEngine() (stop func(), base string, err error) {
	apiSrv := api.NewServer(load.NewRESTAdapter(30 * time.Second))
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", apiSrv.Handler()))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("start in-process engine: %w", err)
	}
	httpSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()

	stop = func() {
		// Each shutdown gets its own budget: sharing one context means a slow API
		// drain could consume the whole deadline and leave the HTTP server's
		// Shutdown zero time, leaking the listener.
		actx, ac := context.WithTimeout(context.Background(), 5*time.Second)
		defer ac()
		_ = apiSrv.Shutdown(actx)
		hctx, hc := context.WithTimeout(context.Background(), 5*time.Second)
		defer hc()
		_ = httpSrv.Shutdown(hctx)
	}
	return stop, "http://" + ln.Addr().String(), nil
}

// driveRun creates the experiment, starts the run, and polls the report until
// the run reaches a terminal state (or ctx expires).
func driveRun(ctx context.Context, base string, spec api.RunSpec) (cliReport, error) {
	var created struct {
		ID string `json:"id"`
	}
	if err := postJSON(ctx, base+"/api/experiments", spec, &created); err != nil {
		return cliReport{}, fmt.Errorf("create experiment: %w", err)
	}
	var started struct {
		RunID string `json:"runId"`
	}
	if err := postJSON(ctx, base+"/api/experiments/"+created.ID+"/run", nil, &started); err != nil {
		return cliReport{}, fmt.Errorf("start run: %w", err)
	}

	reportURL := base + "/api/runs/" + started.RunID + "/report"
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		var rep cliReport
		if err := getJSON(ctx, reportURL, &rep); err != nil {
			return cliReport{}, err
		}
		switch rep.Run.Status {
		case "completed", "failed", "killed":
			return rep, nil
		}
		select {
		case <-ctx.Done():
			return cliReport{}, fmt.Errorf("run did not finish within the timeout")
		case <-ticker.C:
		}
	}
}

// cliReport is the slice of the report the CLI prints. It mirrors the control
// plane's JSON without importing every type, so the CLI stays decoupled.
type cliReport struct {
	Run struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		KillReason string `json:"killReason"`
		Mode       string `json:"mode"`
	} `json:"run"`
	Stats struct {
		Total        int            `json:"total"`
		Errors       int            `json:"errors"`
		ErrorRate    float64        `json:"errorRate"`
		P50          float64        `json:"p50"`
		P95          float64        `json:"p95"`
		P99          float64        `json:"p99"`
		Max          float64        `json:"max"`
		StatusCounts map[string]int `json:"statusCounts"`
	} `json:"stats"`
	Findings []cliFinding `json:"findings"`
	Workers  int          `json:"workers"`
}

// cliFinding is one finding as the CLI consumes it.
type cliFinding struct {
	Category    string `json:"category"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	EvidenceRef string `json:"evidenceRef"`
}

// printReport renders a human-readable summary of the run and its findings.
func printReport(r cliReport) {
	mode := r.Run.Mode
	if mode == "" {
		mode = "local"
	}
	if r.Workers > 0 {
		mode = fmt.Sprintf("%s, %d worker(s)", mode, r.Workers)
	}
	fmt.Printf("Run %s — %s · %s\n", r.Run.ID, r.Run.Status, mode)
	if r.Run.KillReason != "" {
		fmt.Printf("  reason: %s\n", r.Run.KillReason)
	}
	fmt.Printf("  requests=%d  errors=%d (%.1f%%)  p50=%.0fms p95=%.0fms p99=%.0fms max=%.0fms\n",
		r.Stats.Total, r.Stats.Errors, r.Stats.ErrorRate*100,
		r.Stats.P50, r.Stats.P95, r.Stats.P99, r.Stats.Max)
	if len(r.Stats.StatusCounts) > 0 {
		fmt.Printf("  status: %s\n", formatStatusCounts(r.Stats.StatusCounts))
	}

	if len(r.Findings) == 0 {
		fmt.Println("\nNo findings — the target handled this traffic cleanly.")
		return
	}
	fmt.Printf("\nFindings (%d):\n", len(r.Findings))
	for _, f := range r.Findings {
		ref := ""
		if f.EvidenceRef != "" {
			ref = " [" + f.EvidenceRef + "]"
		}
		fmt.Printf("  • [%s] %s: %s%s\n", strings.ToUpper(f.Severity), f.Category, f.Description, ref)
	}
}

// formatStatusCounts renders status tallies in ascending code order: "200:313 500:8".
func formatStatusCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func postJSON(ctx context.Context, url string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return doJSON(req, out)
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return doJSON(req, out)
}

// httpClient bounds each report-poll request on its own. The outer ctx still
// bounds the whole run, but without a per-request timeout a single stalled
// connection (a half-open socket that never sends bytes) could hang the poll
// loop until the run timeout — far longer than any one request should take.
var httpClient = &http.Client{Timeout: 10 * time.Second}

func doJSON(req *http.Request, out any) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// Surface a truncated/partial read as the network error it is, rather than
		// letting a half-read body fall through into a confusing JSON decode error.
		return fmt.Errorf("read response from %s: %w", req.URL.Path, err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode response from %s: %w", req.URL.Path, err)
		}
	}
	return nil
}
