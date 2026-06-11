package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/api"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// reproSpec builds the minimal two-node RunSpec (a -> b) the reproduce CLI
// tests drive: every user walks a then b against the given SUT.
func reproSpec(sutURL string, users int) api.RunSpec {
	vus := make([]load.VirtualUser, users)
	for i := range vus {
		vus[i] = load.VirtualUser{ID: fmt.Sprintf("u%d", i)}
	}
	return api.RunSpec{
		Experiment: domain.Experiment{
			Name: "repro", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: users, AuthStrategy: domain.CredPool},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL: sutURL, Allowlist: []string{"127.0.0.1"},
			RateCap: domain.RateCap{MaxRPS: 10000, MaxConcurrency: 1000}, EnvClass: domain.EnvDev,
		},
		Graph: domain.ScenarioGraph{
			ID:    "g",
			Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "tb"}},
			Edges: []domain.Edge{{From: "a", To: "b", Weight: 1.0}},
		},
		Templates: map[domain.ID]domain.APITemplate{
			"ta": {Method: "GET", Path: "/a"},
			"tb": {Method: "GET", Path: "/b"},
		},
		Start: "a", MaxSteps: 5, Users: vus, Seed: 1,
	}
}

// runOnEngine boots an in-process engine, drives the spec to a finished run on
// it, and returns the engine base URL, the run's report and a stop func.
func runOnEngine(t *testing.T, spec api.RunSpec) (base string, rep cliReport, stop func()) {
	t.Helper()
	stop, base, err := startInProcessEngine()
	if err != nil {
		t.Fatalf("start engine: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep, err = driveRun(ctx, base, spec)
	if err != nil {
		stop()
		t.Fatalf("drive run: %v", err)
	}
	return base, rep, stop
}

// TestReproduceCLIFunctional drives `tmula reproduce` end to end against an
// always-failing SUT: the finding reproduces on every isolated attempt, the
// table says so, and the stored finding is annotated functional (visible
// through the report the engine serves afterwards).
func TestReproduceCLIFunctional(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/b") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	base, rep, stop := runOnEngine(t, reproSpec(sut.URL, 5))
	defer stop()

	out := captureStdout(t, func() error {
		return runReproduce([]string{"--engine", base, "--run", rep.Run.ID, "--finding", "contract/b"})
	})
	// "session u" matches whichever pool user the evidence chose as earliest —
	// sessions complete concurrently, so the representative is not fixed.
	for _, want := range []string{"functional", "3/3", "session u", "a:200", "b:500", "Verdict", "signal, not a proof"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// The annotation reaches the persisted finding the report is rebuilt from.
	var after cliReport
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := getJSON(ctx, base+"/api/runs/"+rep.Run.ID+"/report", &after); err != nil {
		t.Fatalf("refetch report: %v", err)
	}
	found := false
	for _, f := range after.Findings {
		if f.Category == "contract" && f.EvidenceRef == "b" {
			found = true
			if f.RootCauseClass != "functional" {
				t.Errorf("finding rootCauseClass = %q, want functional", f.RootCauseClass)
			}
		}
	}
	if !found {
		t.Fatalf("contract/b finding missing from refetched report: %+v", after.Findings)
	}
}

// TestReproduceCLILoadDependent: the SUT failed under the run's load but is
// healthy when replayed alone, so the CLI reports a load-dependent verdict.
func TestReproduceCLILoadDependent(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() && strings.HasSuffix(r.URL.Path, "/b") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	base, rep, stop := runOnEngine(t, reproSpec(sut.URL, 5))
	defer stop()
	failing.Store(false)

	out := captureStdout(t, func() error {
		return runReproduce([]string{"--engine", base, "--run", rep.Run.ID, "--finding", "contract/b", "--attempts", "4"})
	})
	for _, want := range []string{"load-dependent", "0/4"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestReproduceCLIIndexSelector: --finding also accepts the 1-based position
// of the finding in the run's findings list, matching the order `tmula run`
// prints them in.
func TestReproduceCLIIndexSelector(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sut.Close()

	base, rep, stop := runOnEngine(t, reproSpec(sut.URL, 5))
	defer stop()
	if len(rep.Findings) == 0 {
		t.Fatal("run produced no findings to select by index")
	}

	out := captureStdout(t, func() error {
		return runReproduce([]string{"--engine", base, "--run", rep.Run.ID, "--finding", "1"})
	})
	first := rep.Findings[0]
	if !strings.Contains(out, first.Category+"/"+first.EvidenceRef) {
		t.Errorf("index 1 should resolve to %s/%s:\n%s", first.Category, first.EvidenceRef, out)
	}

	// Out-of-range index fails with the run's finding count, not a server error.
	_, err := captureStdoutErr(t, func() error {
		return runReproduce([]string{"--engine", base, "--run", rep.Run.ID, "--finding", "99"})
	})
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("out-of-range index err = %v, want an out-of-range message", err)
	}
}

// TestReproduceCLIArgErrors covers flag validation: the required trio, and a
// selector that is neither category/evidenceRef nor an index.
func TestReproduceCLIArgErrors(t *testing.T) {
	if err := runReproduce([]string{}); err == nil {
		t.Error("missing required flags should error")
	}
	if err := runReproduce([]string{"--engine", "http://e", "--run", "r"}); err == nil {
		t.Error("missing --finding should error")
	}
	if err := runReproduce([]string{"--engine", "http://e", "--run", "r", "--finding", "no-slash"}); err == nil {
		t.Error("malformed --finding selector should error")
	}
	if err := runReproduce([]string{"--engine", "http://e", "--run", "r", "--finding", "contract/b", "extra"}); err == nil {
		t.Error("positional arguments should error")
	}
}
