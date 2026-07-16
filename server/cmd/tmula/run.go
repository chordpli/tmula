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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chordpli/tmula/server/internal/api"
	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/gate"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/scenariofile"
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
		baselineRun    = fs.String("baseline", "", "regression gate: baseline run id, fetched from --engine; exits non-zero only on findings new vs the baseline")
		baselineFile   = fs.String("baseline-file", "", "regression gate: baseline report JSON file (a previous `tmula run --json` output)")
		knownIssues    = fs.String("known-issues", "", "known-issues YAML; matching new findings are suppressed in the baseline gate until their expires date (YYYY-MM-DD)")
		summary        = fs.String("summary", "", "append a markdown run summary to this file (default: $GITHUB_STEP_SUMMARY when set)")
		keepAccounts   = fs.Bool("keep-accounts", false, "bootstrap-signup: leave the provisioned accounts in place instead of deprovisioning them (the only way to run a signup flow that declares no teardown)")
		authSource     = fs.String("auth-source", "", "attach an external credential pool without editing the scenario: file:./pool.csv or env:VAR. Resolved in-process (the secret never crosses the wire); overrides any auth block the scenario declares")
		authFormat     = fs.String("auth-format", "", "credential body format for --auth-source: csv | jsonl | tokens (default: inferred from a .csv/.jsonl file extension, else tokens)")
		timeout        = fs.Duration("timeout", 2*time.Minute, "max time to wait for the run to finish")
		allowExec      = fs.Bool("allow-exec", false, "permit the exec credential strategy, which runs an operator-supplied LOCAL command per virtual user to mint a token. OFF by default: a scenario file is untrusted, and an exec command's network egress bypasses the target allowlist/rate cap")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: tmula run [scenario.yaml] [flags]\n\n"+
			"  tmula run scenario.yaml --users 50\n"+
			"  tmula run --target http://localhost:9000 --get /health --users 20\n"+
			"  tmula run scenario.yaml --open 278 --for 3600\n\n"+
			"exit codes: 0 ok · 1 error · 2 findings (with --fail-on-findings) · 3 new findings (with --baseline)\n\n")
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

	// The baseline gate's inputs are validated and loaded before the run: a
	// mistyped path or a malformed known-issues file must fail in seconds, not
	// after minutes of load generation.
	if *baselineRun != "" && *baselineFile != "" {
		return fmt.Errorf("use only one of --baseline (run id via --engine) or --baseline-file (report JSON)")
	}
	if *baselineRun != "" && *engine == "" {
		return fmt.Errorf("--baseline takes a run id from a long-running engine; pass --engine, or use --baseline-file with a saved report (an in-process run starts with empty history)")
	}
	hasBaseline := *baselineRun != "" || *baselineFile != ""
	if *knownIssues != "" && !hasBaseline {
		return fmt.Errorf("--known-issues only affects the baseline gate; pass --baseline or --baseline-file")
	}
	var known []gate.KnownIssue
	if *knownIssues != "" {
		var err error
		if known, err = loadKnownIssues(*knownIssues); err != nil {
			return err
		}
	}
	var baseFindings []cliFinding
	baseLabel := ""
	if *baselineFile != "" {
		var err error
		if baseFindings, baseLabel, err = loadBaselineFile(*baselineFile); err != nil {
			return err
		}
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
	// Resolve an external auth source (auth.source.file) against the scenario
	// file's own directory, so a relative path is read predictably from beside the
	// scenario and confined there. Single-endpoint mode has no file: dir is empty
	// and ExpandFrom falls back to the working directory.
	scenarioDir := ""
	if file != "" {
		scenarioDir = filepath.Dir(file)
	}
	// --auth-source supplies the whole credential pool from the flag, so the scenario's
	// own auth block is ignored entirely. Drop it BEFORE Expand: an importer-scaffolded
	// scenario still carries REPLACE_ME_* placeholders that Expand would reject, and the
	// operator asked to supply the credential via the flag instead of editing them — so
	// the placeholder must not fail the expand the flag was meant to sidestep.
	if *authSource != "" {
		sc.Auth = nil
	}
	// Against a remote --engine, a source-backed pool ships its reference-only
	// SourceRef (the engine's workers resolve it locally) instead of being read
	// into entries here: only the reference crosses the wire, and the CLI need not
	// even hold the credential file. Every other auth form (inline users, login)
	// still expands to in-process secrets and is rejected against --engine below.
	spec, err := scenariofile.ExpandFrom(sc, scenarioDir)
	if *engine != "" && sourceBackedAuth(sc.Auth) {
		spec, err = scenariofile.ExpandRef(sc, scenarioDir)
	}
	if err != nil {
		return err
	}

	// --auth-source attaches (or overrides) the run's credential pool from a flag,
	// so an operator can authenticate a scenario without editing it. It is resolved
	// the same way P1 resolves a file/env source at expand time — in-process, into
	// inline entries — so the secret never crosses the wire. The flag wins over any
	// auth block the scenario declares (documented on the flag). It is resolved
	// against the working directory (a flag path is operator-supplied at the CLI,
	// not relative to the scenario file), confined there by auth.FileSource.
	if *authSource != "" {
		pool, err := resolveAuthSourceFlag(*authSource, *authFormat)
		if err != nil {
			return err
		}
		spec.CredentialPool = pool
		spec.LoginFlow = nil // a flag pool is pre-supplied entries, never a login flow
		spec.Experiment.Params.AuthStrategy = pool.Strategy
	}

	// --keep-accounts opts a bootstrap-signup run out of teardown. It is the only
	// escape from the gating-safety rule that a signup flow with no teardown is
	// rejected, so apply it before validation. It is meaningful only for a bootstrap
	// pool; on any other pool it is inert (the field is ignored by other strategies).
	if *keepAccounts && spec.CredentialPool != nil {
		spec.CredentialPool.KeepAccounts = true
	}

	// A mint run against a managed IdP is the #1 mint footgun: the importer flagged that
	// the token issuer's signing key is not the operator's, so tokens tmula forges will be
	// rejected. Warn loudly (but do not block) before sending traffic that will 401.
	warnMintManagedIdP(spec)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// A run-id baseline resolves through the engine before the run starts, for
	// the same fail-fast reason as the file checks above.
	if *baselineRun != "" {
		var err error
		if baseFindings, baseLabel, err = fetchBaselineRun(ctx, *engine, *baselineRun); err != nil {
			return err
		}
	}

	// In-process is the default: drive the control plane through its Go API so a
	// scenario's credential secrets never have to cross the wire (the credential
	// secret carries json:"-", so an HTTP submission would silently strip it). Only
	// when the operator points at a separate --engine do we go over HTTP, where the
	// pool is not yet supported for exactly that reason.
	var report cliReport
	if *engine == "" {
		if spec.CredentialPool == nil {
			// No auth: keep the original behavior of booting a real loopback engine
			// and driving it over HTTP, so the in-process and remote paths stay
			// identical for the common (unauthenticated) case.
			stop, base, err := startInProcessEngine()
			if err != nil {
				return err
			}
			defer stop()
			report, err = driveRun(ctx, base, spec)
			if err != nil {
				return err
			}
		} else {
			srv := api.NewServer(load.NewRESTAdapter(30*time.Second), api.WithAllowExec(*allowExec))
			defer func() {
				sctx, sc := context.WithTimeout(context.Background(), 5*time.Second)
				defer sc()
				_ = srv.Shutdown(sctx)
			}()
			report, err = driveRunInProcess(ctx, srv, spec)
			if err != nil {
				return err
			}
		}
	} else {
		// A source-backed pool is allowed against a remote engine: it carries only a
		// reference-only SourceRef (no secret), which the engine's distributed
		// workers resolve locally and assign by global index. Any pool that would put
		// a secret on the wire — inline entries or a minted login token — is still
		// refused; run in-process to authenticate with those. A bootstrap-signup pool
		// provisions per-node accounts and likewise has no shared reference to fan out
		// (distributed/remote bootstrap is a follow-up), so it too runs in-process only.
		if spec.CredentialPool != nil && spec.CredentialPool.Source == nil {
			return fmt.Errorf("a credential pool is not supported against a remote --engine (the secret cannot cross the wire, and bootstrap-signup provisions per-node accounts); run in-process to authenticate")
		}
		report, err = driveRun(ctx, *engine, spec)
		if err != nil {
			return err
		}
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
	// The baseline gate verdict is computed before the summary so the four-way
	// table lands in it. Expired-suppression warnings go to stderr in any mode;
	// the human rendering is skipped under --json (stdout is the report
	// document there — the verdict still reaches CI via the exit code and the
	// markdown summary).
	var gateRes *gate.Result
	if hasBaseline {
		res := gate.Evaluate(toDomainFindings(baseFindings), toDomainFindings(report.Findings), known, time.Now().UTC())
		gateRes = &res
		warnExpired(res.Expired)
		if !*asJSON {
			printGateResult(res, baseLabel)
		}
	}
	// The markdown summary is written before the failed/killed and gate checks
	// below: a broken run is exactly when the summary is needed (it is what a CI
	// job shows after the gate makes it exit non-zero). It is best-effort — a
	// summary-file problem must never mask the run's real outcome or downgrade
	// the gate's exit code, so a write failure is reported but not returned.
	if path := summaryPath(*summary); path != "" {
		md := markdownReport(report)
		if gateRes != nil {
			md += "\n" + markdownBaselineGate(*gateRes, baseLabel)
		}
		if err := writeSummary(path, md); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		} else {
			// A breadcrumb, also for the local user who happens to have
			// GITHUB_STEP_SUMMARY exported and would otherwise be appending to it
			// silently.
			fmt.Fprintf(os.Stderr, "markdown summary appended to %s\n", path)
		}
	}

	// Exit-code ladder, in precedence order — a later gate is consulted only
	// when every earlier one passed:
	//   1. exit 1: the run did not complete cleanly (failed/killed). Fail loud,
	//      unconditionally — no gate may make a broken run look green.
	//   2. exit 2: --fail-on-findings / --fail-on-severity, the absolute gate.
	//      It fails on any (matching) finding, baseline or not, so it keeps its
	//      meaning even when a --baseline is also passed.
	//   3. exit 3: --baseline / --baseline-file, the regression gate. It fails
	//      only on findings new vs the baseline, after known-issue suppression.
	//
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
	if gateRes != nil && len(gateRes.New) > 0 {
		return fmt.Errorf("%w (%d)", errNewFindings, len(gateRes.New))
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

// warnMintManagedIdP prints a stderr warning when a mint run coexists with a
// mint-managed-idp advisory: the importer flagged that the token issuer's signing key is
// not the operator's, so a forged token will be rejected. It warns rather than blocks —
// an operator who genuinely holds the key (a self-hosted issuer the importer could not
// tell apart) may proceed — but the footgun is surfaced before any traffic is sent.
func warnMintManagedIdP(spec api.RunSpec) {
	if spec.CredentialPool == nil || spec.CredentialPool.Strategy != domain.CredMint {
		return
	}
	for _, adv := range spec.AuthAdvisories {
		if adv.Code == domain.AdvisoryMintManagedIDP {
			fmt.Fprintf(os.Stderr, "warning: %s\n", adv.Message())
		}
	}
}

// sourceBackedAuth reports whether the scenario's auth is a pool backed by an
// external SOURCE (a file or env reference) rather than inline secrets. Only this
// form may fan out to a remote engine: it carries a reference, never a secret. An
// inline-users pool or a login flow is not source-backed (their secrets are
// in-process), so it stays rejected against --engine.
func sourceBackedAuth(a *scenariofile.Auth) bool {
	if a == nil || a.Source == nil {
		return false
	}
	// The pool strategy (empty defaults to "pool") is the only one that takes a
	// source; login/bootstrap with a source is a malformed document that expand
	// rejects, so it never reaches the engine as a source pool.
	return a.Strategy == "" || a.Strategy == string(domain.CredPool)
}

// resolveAuthSourceFlag turns a --auth-source value ("file:./pool.csv" or
// "env:VAR") plus an optional --auth-format into a fully resolved pre-supplied
// credential pool. It resolves the source the SAME way scenariofile.Expand
// resolves a scenario auth.source — in-process, into inline domain.Credential
// entries — so the secret stays on this host and never crosses the wire. A file
// path is rooted at the working directory (a flag path is supplied at the CLI,
// not beside the scenario) and confined there by auth.FileSource's guards.
//
// The format is inferred when --auth-format is empty: a .csv or .jsonl file
// extension picks csv/jsonl, otherwise tokens (one secret per line) — env has no
// extension to infer from, so it too defaults to tokens. An unknown scheme or a
// missing scheme separator is a clear flag error.
func resolveAuthSourceFlag(flagVal, format string) (*domain.CredentialPool, error) {
	scheme, ref, ok := strings.Cut(flagVal, ":")
	if !ok || ref == "" {
		return nil, fmt.Errorf("--auth-source %q must be file:<path> or env:<VAR>", flagVal)
	}

	f := auth.Format(format)
	if format == "" {
		f = inferAuthFormat(scheme, ref)
	}

	var src auth.CredentialSource
	switch scheme {
	case "file":
		// A relative flag path is rooted at the working directory; an absolute path
		// is rooted at its own directory so an operator may point anywhere on the
		// host (a CLI flag is trusted operator input, unlike a scenario-embedded
		// path). Either way auth.FileSource's containment/symlink/size guards apply.
		root, path := ".", ref
		if filepath.IsAbs(ref) {
			root, path = filepath.Dir(ref), filepath.Base(ref)
		}
		src = auth.FileSource{Root: root, Path: path, Format: f}
	case "env":
		src = auth.EnvSource{Var: ref, Format: f}
	default:
		return nil, fmt.Errorf("--auth-source scheme %q is not supported (use file: or env:)", scheme)
	}

	entries, err := src.Load(context.Background())
	if err != nil {
		return nil, err
	}
	return &domain.CredentialPool{ID: "cli-pool", Strategy: domain.CredPool, Entries: entries}, nil
}

// inferAuthFormat picks a credential format from a --auth-source reference when
// --auth-format is omitted: a .csv or .jsonl file extension selects that format,
// otherwise (any other extension, or an env var with no extension) it falls back
// to tokens — one secret per line.
func inferAuthFormat(scheme, ref string) auth.Format {
	if scheme == "file" {
		switch strings.ToLower(filepath.Ext(ref)) {
		case ".csv":
			return auth.CSV
		case ".jsonl":
			return auth.JSONL
		}
	}
	return auth.Tokens
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

// driveRunInProcess creates, starts and polls a run entirely through the control
// plane's Go API, so a spec carrying credential secrets never crosses the wire.
// It mirrors driveRun's create→start→poll loop and converts the typed report into
// the CLI's view via the same JSON shape the HTTP path serves, keeping the CLI's
// report struct decoupled from the control-plane types.
func driveRunInProcess(ctx context.Context, srv *api.Server, spec api.RunSpec) (cliReport, error) {
	expID, err := srv.CreateExperiment(spec)
	if err != nil {
		return cliReport{}, fmt.Errorf("create experiment: %w", err)
	}
	runID, err := srv.StartRun(expID)
	if err != nil {
		return cliReport{}, fmt.Errorf("start run: %w", err)
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		rep, ok := srv.Report(runID)
		if !ok {
			return cliReport{}, fmt.Errorf("run %s not found", runID)
		}
		switch rep.Run.Status {
		case "completed", "failed", "killed":
			return toCLIReport(rep)
		}
		select {
		case <-ctx.Done():
			return cliReport{}, fmt.Errorf("run did not finish within the timeout")
		case <-ticker.C:
		}
	}
}

// toCLIReport converts a typed control-plane report into the CLI's view by
// round-tripping the exact JSON the HTTP report endpoint serves, so the in-process
// and over-HTTP paths print identically. The report never contains a secret (the
// credential secret is json:"-"), so this carries no sensitive data.
func toCLIReport(rep api.Report) (cliReport, error) {
	b, err := json.Marshal(rep)
	if err != nil {
		return cliReport{}, fmt.Errorf("encode report: %w", err)
	}
	var out cliReport
	if err := json.Unmarshal(b, &out); err != nil {
		return cliReport{}, fmt.Errorf("decode report: %w", err)
	}
	return out, nil
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
	// Notes are non-failing, observability-only run remarks (e.g. "auth may have
	// expired" on a cluster of 401/403s). They are surfaced to the operator but
	// never gate the run.
	Notes []string `json:"notes"`
	// ServerMetrics / MetricsError mirror the report's optional Prometheus
	// correlation (RunSpec metrics opt-in); the markdown summary tabulates them.
	ServerMetrics []cliMetricSeries `json:"serverMetrics"`
	MetricsError  string            `json:"metricsError"`
}

// cliMetricSeries is one fetched server-side series as the CLI consumes it.
type cliMetricSeries struct {
	Name   string `json:"name"`
	Points []struct {
		TS int64   `json:"ts"`
		V  float64 `json:"v"`
	} `json:"points"`
}

// cliFinding is one finding as the CLI consumes it.
type cliFinding struct {
	Category    string `json:"category"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	EvidenceRef string `json:"evidenceRef"`
	// RootCauseClass is the `tmula reproduce` verdict when one was recorded:
	// functional / load-dependent / flaky.
	RootCauseClass string `json:"rootCauseClass"`
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

	// Run-level notes are non-failing observability remarks (e.g. an auth-expiry
	// hint). They print between the stats and the findings, prefixed so an operator
	// reads them as advice, not a finding.
	for _, n := range r.Notes {
		fmt.Printf("  note: %s\n", n)
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
		// A root-cause class only exists after a `tmula reproduce` pass
		// annotated the stored finding; surface it so a refetched report shows
		// the triage already done.
		cause := ""
		if f.RootCauseClass != "" {
			cause = " (root cause: " + f.RootCauseClass + ")"
		}
		fmt.Printf("  • [%s] %s: %s%s%s\n", strings.ToUpper(f.Severity), f.Category, f.Description, ref, cause)
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
