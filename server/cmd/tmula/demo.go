package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/chordpli/tmula/server/internal/api"
	"github.com/chordpli/tmula/server/internal/demo"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/importer"
	"github.com/chordpli/tmula/server/internal/scenariofile"
	"github.com/chordpli/tmula/server/internal/web"
)

// Demo pacing: the learner's open-workload suggestion mirrors the (sparse,
// human-paced) example log, so the demo compresses it into its window — same
// learned graph and weights, miniature timing — instead of replaying a
// morning's traffic in real time.
const (
	// demoMinSessions is the minimum number of sessions the demo schedules
	// across its window. The planted bugs fire at a few percent per request,
	// so volume is what makes at least one finding a statistical certainty
	// (~190 cart calls at 8% leave odds of silence below 1e-6).
	demoMinSessions = 300
	// demoMinRate floors the arrival rate so a long demo still animates the
	// live flow map instead of trickling one user at a time.
	demoMinRate = 8.0
	// demoMaxRate caps the arrival rate; the demo is a tour, not a stress test.
	demoMaxRate = 400.0
	// demoThinkDivisor bounds per-step think time to duration/N, so a whole
	// session always finishes well inside the demo window.
	demoThinkDivisor = 20
	// demoTraceConcurrency is the open model's back-pressure cap, chosen to sit
	// within the control plane's per-request trace limit (traceMaxUsers = 200)
	// so the live flow map streams every step.
	demoTraceConcurrency = 200
	// demoRunGrace bounds how long past --duration the demo waits for in-flight
	// sessions to drain before giving up on the run.
	demoRunGrace = 60 * time.Second
)

// demoHeartbeat is how often the [4/4] wait prints a one-line progress
// heartbeat (elapsed over the window plus live counts), so the demo visibly
// progresses without flooding the terminal. A var so tests can compress it.
var demoHeartbeat = 5 * time.Second

// runDemo implements `tmula demo`: the whole tmula loop in one command. It
// boots a tiny shop SUT with planted bugs, learns a behavior graph + open
// workload from the embedded access log, starts the local engine with the web
// console, replays the learned traffic against the shop, and prints the
// findings with concrete next steps.
func runDemo(args []string) error {
	fs := flag.NewFlagSet("tmula demo", flag.ContinueOnError)
	var (
		addr = fs.String("addr", ":8080", "HTTP listen address for the demo engine + web console")
		// 30s keeps the first-run experience tight: the pacing floor schedules
		// enough sessions inside that window for every planted bug to surface.
		duration  = fs.Duration("duration", 30*time.Second, "how long the learned traffic keeps arriving")
		noBrowser = fs.Bool("no-browser", false, "do not open the web console in a browser")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: tmula demo [--addr :8080] [--duration 30s] [--no-browser]\n\n"+
			"Starts a demo shop with planted bugs, learns a behavior graph from its\n"+
			"access log, replays the learned traffic against it (live web console\n"+
			"included), and prints the findings — no setup, no other terminal.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *duration <= 0 {
		return fmt.Errorf("--duration must be positive, got %v", *duration)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	_, err := runDemoWith(ctx, demoOptions{
		addr:        *addr,
		duration:    *duration,
		noBrowser:   *noBrowser,
		keepAlive:   true,
		openBrowser: openBrowser,
	})
	return err
}

// demoOptions parameterizes the demo pipeline for tests: an injectable browser
// opener (so CI never pops a window) and keepAlive off (so a dry run returns
// instead of waiting for Ctrl-C).
type demoOptions struct {
	addr        string        // engine + console listen address
	duration    time.Duration // arrival window of the demo run
	noBrowser   bool          // skip opening the console in a browser
	keepAlive   bool          // after the summary, stay up until ctx is done
	openBrowser func(url string) error
}

// runDemoWith runs the demo pipeline under ctx and returns the run's report.
// Cancelling ctx (Ctrl-C) at any stage exits cleanly: the deferred stops shut
// the engine (draining the in-flight run) and the shop down gracefully.
func runDemoWith(ctx context.Context, opts demoOptions) (cliReport, error) {
	// failOr maps a stage error to the demo's exit: a cancelled ctx means the
	// user hit Ctrl-C, which is a clean shutdown, not a failure.
	failOr := func(stage string, err error) (cliReport, error) {
		if ctx.Err() != nil {
			fmt.Println("\ndemo interrupted — shutting down")
			return cliReport{}, nil
		}
		return cliReport{}, fmt.Errorf("demo: %s: %w", stage, err)
	}

	fmt.Print("tmula demo — learn from traffic, replay it, catch the bugs\n\n")

	// The shop binds an ephemeral loopback port, so the only port the user can
	// ever collide on is the engine's --addr.
	stopShop, sutURL, err := startDemoShop()
	if err != nil {
		return cliReport{}, err
	}
	defer stopShop()
	fmt.Printf("[1/4] demo shop running at %s\n", sutURL)
	fmt.Print("      (a tiny store with planted bugs: a flaky cart, a checkout that degrades under load, a rare broken product link)\n")

	spec, stats, err := buildDemoSpec(sutURL, opts.duration)
	if err != nil {
		return cliReport{}, err
	}
	fmt.Printf("[2/4] learned a behavior graph from its access log: %d endpoint(s) from %d request(s) across %d session(s)\n",
		len(spec.Templates), stats.Requests, stats.Sessions)

	stopEngine, engineURL, err := startDemoEngine(opts.addr)
	if err != nil {
		return cliReport{}, err
	}
	defer stopEngine()

	runCtx, cancel := context.WithTimeout(ctx, opts.duration+demoRunGrace)
	defer cancel()
	var created struct {
		ID string `json:"id"`
	}
	if err := postJSON(runCtx, engineURL+"/api/experiments", spec, &created); err != nil {
		return failOr("create experiment", err)
	}
	var started struct {
		RunID string `json:"runId"`
	}
	if err := postJSON(runCtx, engineURL+"/api/experiments/"+created.ID+"/run", nil, &started); err != nil {
		return failOr("start run", err)
	}
	// Fixed contract with the web console: ?run=<run-id> attaches it straight
	// to this run's live view (traffic flow map + live metrics) instead of the
	// setup form, so the demo's browser tab lands on the action.
	consoleURL := demoConsoleURL(engineURL, started.RunID)
	fmt.Printf("[3/4] engine + web console at %s — run %s replays the learned traffic for %v\n",
		engineURL, started.RunID, opts.duration)
	fmt.Printf("      live view: %s\n", consoleURL)
	if !web.HasBuiltUI() {
		fmt.Println("      note: this binary serves the placeholder UI — the live console needs a binary built with `make web`")
	}

	if !opts.noBrowser && opts.openBrowser != nil {
		if err := opts.openBrowser(consoleURL); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not open a browser: %v\n", err)
		}
	}
	fmt.Printf("[4/4] watching the run — the findings summary prints here when the %v window closes\n\n", opts.duration)

	rep, err := pollRunReport(runCtx, engineURL, started.RunID, opts.duration)
	if err != nil {
		return failOr("watch run", err)
	}
	printReport(rep)
	printDemoNextSteps(engineURL, started.RunID, rep)

	if opts.keepAlive {
		fmt.Println("\nThe demo stays up so the commands above keep working — press Ctrl-C to stop (shuts down the engine and the demo shop).")
		<-ctx.Done()
		fmt.Println("\nshutting down")
	}
	return rep, nil
}

// buildDemoSpec learns the demo's run spec from the embedded access log: the
// importer turns the traffic into a weighted behavior graph plus an open
// workload suggestion, the pacing is compressed into the demo window, and the
// result expands through the same scenariofile path every `tmula run` uses —
// allowlist defaulting (the SUT host only) and graph validation included, so
// the demo never sidesteps the safety guard.
func buildDemoSpec(targetURL string, d time.Duration) (api.RunSpec, importer.AccessLogStats, error) {
	sc, stats, err := importer.FromAccessLog(demo.AccessLog)
	if err != nil {
		return api.RunSpec{}, stats, fmt.Errorf("demo: learn from the embedded access log: %w", err)
	}
	sc.Target = targetURL
	if sc.Open == nil {
		// FromAccessLog always suggests an open workload; guard anyway so the
		// pacing below cannot nil-panic if that ever changes.
		sc.Open = &scenariofile.Open{}
	}
	adaptDemoPacing(sc.Open, d)
	spec, err := scenariofile.Expand(sc)
	if err != nil {
		return api.RunSpec{}, stats, fmt.Errorf("demo: expand learned scenario: %w", err)
	}
	spec.Experiment.Name = "demo"
	// Live per-request tracing powers the console's flow map; the pacing keeps
	// MaxConcurrency within the trace limit so it stays on.
	spec.Trace = true
	return spec, stats, nil
}

// adaptDemoPacing compresses the learner's open-workload suggestion into the
// demo window: the arrival window becomes the --duration, the rate is floored
// so every planted bug surfaces (and capped so the demo stays a tour), and the
// think time scales down proportionally so sessions finish inside the window.
// The learned graph and its weights are untouched — only timing is miniature.
func adaptDemoPacing(o *scenariofile.Open, d time.Duration) {
	forSec := int(math.Ceil(d.Seconds()))
	if forSec < 1 {
		forSec = 1
	}
	o.ForSeconds = forSec
	// The demo always runs a constant arrival rate; clear any ramp fields so
	// the rate below is authoritative.
	o.From, o.To = 0, 0
	rate := math.Max(o.Rate, math.Max(demoMinRate, demoMinSessions/float64(forSec)))
	o.Rate = math.Min(rate, demoMaxRate)

	maxThink := int(d.Milliseconds() / demoThinkDivisor)
	if len(o.ThinkMs) == 2 && o.ThinkMs[1] > maxThink {
		// Scale both ends by the same factor so the learned min:max character
		// survives the compression.
		f := float64(maxThink) / float64(o.ThinkMs[1])
		o.ThinkMs = []int{int(float64(o.ThinkMs[0]) * f), maxThink}
	}
	o.MaxConcurrency = demoTraceConcurrency
}

// startDemoShop boots the planted-bug shop SUT on an ephemeral loopback port.
// The returned stop func shuts it down gracefully.
func startDemoShop() (stop func(), base string, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("demo: start demo shop: %w", err)
	}
	srv := &http.Server{Handler: demo.NewShop().Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		// ErrServerClosed is the normal Shutdown path; anything else means the
		// SUT died mid-demo and is worth a trace in the output.
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("demo shop server failed", "err", err)
		}
	}()
	stop = func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
	return stop, "http://" + ln.Addr().String(), nil
}

// startDemoEngine boots the same local engine `tmula` serves by default — the
// control plane under /api, the embedded web console under everything else —
// on the user-chosen address. A taken port fails fast with a pointer at the
// flag that fixes it. The returned stop func drains in-flight runs first, then
// closes the listener (mirroring startInProcessEngine's two budgets).
func startDemoEngine(addr string) (stop func(), base string, err error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("demo: cannot listen on %s (%v); if the port is taken, pass --addr with a free one (e.g. --addr :8081)", addr, err)
	}
	// The same production surface `tmula` serves by default (newEngineServer),
	// so the demo's /api/import carries import coverage stats too.
	apiSrv, handler := newEngineServer(domain.RoleLocal)
	httpSrv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()

	stop = func() {
		actx, ac := context.WithTimeout(context.Background(), 5*time.Second)
		defer ac()
		_ = apiSrv.Shutdown(actx)
		hctx, hc := context.WithTimeout(context.Background(), 5*time.Second)
		defer hc()
		_ = httpSrv.Shutdown(hctx)
	}
	return stop, "http://" + displayHostPort(ln.Addr().String()), nil
}

// displayHostPort rewrites a listener address into one a browser can open: a
// wildcard listen host (":8080" binds every interface, reported as "::" or
// "0.0.0.0") is shown as localhost.
func displayHostPort(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "::", "0.0.0.0":
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// demoConsoleURL builds the web console URL attached to a run's live view —
// the ?run=<run-id> contract the console resolves into the flow map + live
// metrics instead of the setup form.
func demoConsoleURL(engineURL, runID string) string {
	return engineURL + "/?run=" + url.QueryEscape(runID)
}

// pollRunReport polls the run's report until it reaches a terminal state (or
// ctx expires), mirroring driveRun's loop for a run someone else started.
// While waiting it prints a heartbeat line every demoHeartbeat — elapsed over
// the window plus the live request/finding counts — so the [4/4] wait visibly
// progresses. One short line per beat: a same-line \r rewrite would garble
// piped or CI output.
func pollRunReport(ctx context.Context, base, runID string, window time.Duration) (cliReport, error) {
	reportURL := base + "/api/runs/" + runID + "/report"
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(demoHeartbeat)
	defer heartbeat.Stop()
	start := time.Now()
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
			return cliReport{}, fmt.Errorf("run did not finish within the demo window")
		case <-heartbeat.C:
			fmt.Printf("      t+%ds/%ds · requests %d · findings %d\n",
				int(time.Since(start).Seconds()), int(window.Seconds()), rep.Stats.Total, len(rep.Findings))
		case <-ticker.C:
		}
	}
}

// printDemoNextSteps turns the demo's ending into a beginning: the exact
// commands to triage what was just found and to point the same loop at the
// user's own service.
func printDemoNextSteps(engineURL, runID string, rep cliReport) {
	fmt.Println("\nNext steps:")
	if len(rep.Findings) > 0 {
		f := rep.Findings[0]
		fmt.Println("  • triage a finding in isolation (works while the demo is up):")
		fmt.Printf("      tmula reproduce --engine %s --run %s --finding %s/%s\n", engineURL, runID, f.Category, f.EvidenceRef)
	}
	fmt.Printf("  • full report:  %s/api/runs/%s/report.html\n", engineURL, runID)
	fmt.Printf("  • web console:  %s\n", demoConsoleURL(engineURL, runID))
	fmt.Println("  • learn from your own traffic:")
	fmt.Println("      tmula init --from /var/log/nginx/access.log --target https://staging.example.com --out scenario.yaml")
	fmt.Println("      tmula run scenario.yaml")
}

// openBrowser opens url in the platform browser, best-effort: the demo prints
// every URL it opens, so when this fails (headless box, unsupported OS) the
// text is the fallback.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	default:
		return fmt.Errorf("no browser opener for %s", runtime.GOOS)
	}
}
