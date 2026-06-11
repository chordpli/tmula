package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// newCPServer is newCP but also returns the *Server, so reproduce tests can
// call the Go-level entry point and inspect the store behind the same engine
// the HTTP run went through.
func newCPServer(t *testing.T) (*Server, *httptest.Server, func()) {
	t.Helper()
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	cp := httptest.NewServer(srv.Handler())
	return srv, cp, cp.Close
}

// TestReproduceFunctionalVerdict drives the killer path end to end: a SUT that
// fails /b unconditionally produces a contract finding with evidence; replaying
// the evidence session in isolation reproduces the failure on every attempt, so
// the verdict is "functional" (the bug does not need load) — and the verdict is
// annotated on both the live report and the stored finding.
func TestReproduceFunctionalVerdict(t *testing.T) {
	sut := sutFailB()
	defer sut.Close()
	srv, cp, closeCP := newCPServer(t)
	defer closeCP()

	rep := runToReport(t, cp.URL, specFor(sut.URL, 5))

	res, err := srv.ReproduceFinding(context.Background(), rep.Run.ID,
		ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "b"})
	if err != nil {
		t.Fatalf("ReproduceFinding: %v", err)
	}
	if res.RootCauseClass != domain.RootCauseFunctional {
		t.Errorf("rootCauseClass = %q, want %q", res.RootCauseClass, domain.RootCauseFunctional)
	}
	if len(res.Attempts) != 3 || res.Reproduced != 3 {
		t.Errorf("attempts = %d reproduced = %d, want 3/3 (default attempts)", len(res.Attempts), res.Reproduced)
	}
	for i, at := range res.Attempts {
		if !at.Reproduced {
			t.Errorf("attempt %d not reproduced against an always-failing SUT", i+1)
		}
	}
	if res.Note == "" {
		t.Error("result carries no limitation note; the verdict must be framed as a signal, not a proof")
	}

	// The verdict is stamped on the stored finding (system of record) ...
	stored, err := srv.store.Findings(rep.Run.ID)
	if err != nil {
		t.Fatalf("store findings: %v", err)
	}
	if f := findingIn(stored, domain.FindingContract, "b"); f == nil || f.RootCauseClass != domain.RootCauseFunctional {
		t.Errorf("stored finding not annotated: %+v", f)
	}
	// ... and on the live report the engine still serves.
	live, ok := srv.Report(rep.Run.ID)
	if !ok {
		t.Fatal("run vanished from the engine")
	}
	if f := findingWithRef(live, domain.FindingContract, "b"); f == nil || f.RootCauseClass != domain.RootCauseFunctional {
		t.Errorf("live finding not annotated: %+v", f)
	}
}

// TestReproduceLoadDependentVerdict covers the other half of the classification:
// a SUT that failed during the run but is healthy now (the stand-in for a bug
// that needs concurrency/saturation) reproduces on zero attempts, so the
// verdict is "load-dependent".
func TestReproduceLoadDependentVerdict(t *testing.T) {
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
	srv, cp, closeCP := newCPServer(t)
	defer closeCP()

	rep := runToReport(t, cp.URL, specFor(sut.URL, 5))
	failing.Store(false) // the load is gone; so is the failure

	res, err := srv.ReproduceFinding(context.Background(), rep.Run.ID,
		ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "b"})
	if err != nil {
		t.Fatalf("ReproduceFinding: %v", err)
	}
	if res.RootCauseClass != domain.RootCauseLoadDependent {
		t.Errorf("rootCauseClass = %q, want %q", res.RootCauseClass, domain.RootCauseLoadDependent)
	}
	if res.Reproduced != 0 {
		t.Errorf("reproduced = %d, want 0 against a healthy SUT", res.Reproduced)
	}
	if f := findingIn(mustFindings(t, srv, rep.Run.ID), domain.FindingContract, "b"); f == nil || f.RootCauseClass != domain.RootCauseLoadDependent {
		t.Errorf("stored finding not annotated load-dependent: %+v", f)
	}
}

// TestReproduceFlakyVerdict: a SUT that fails the same step on every other
// request reproduces on some attempts but not all, so the verdict is the
// honest middle ground, "flaky".
func TestReproduceFlakyVerdict(t *testing.T) {
	var n atomic.Int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/b") && n.Add(1)%2 == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()
	srv, cp, closeCP := newCPServer(t)
	defer closeCP()

	rep := runToReport(t, cp.URL, specFor(sut.URL, 5))
	n.Store(0) // attempts 1 and 3 hit the failing phase, attempt 2 the healthy one

	res, err := srv.ReproduceFinding(context.Background(), rep.Run.ID,
		ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "b", Attempts: 2})
	if err != nil {
		t.Fatalf("ReproduceFinding: %v", err)
	}
	if res.RootCauseClass != domain.RootCauseFlaky {
		t.Errorf("rootCauseClass = %q, want %q (reproduced %d/%d)", res.RootCauseClass, domain.RootCauseFlaky, res.Reproduced, len(res.Attempts))
	}
	if res.Reproduced != 1 || len(res.Attempts) != 2 {
		t.Errorf("reproduced = %d/%d, want 1/2", res.Reproduced, len(res.Attempts))
	}
}

// TestReproduceWalkIsDeterministic pins the property the whole feature stands
// on: the same (seed, user index) coordinates replay the same walk. On a
// branching graph only some sessions reach the failing node; every reproduce
// attempt must traverse exactly the evidence session's recorded path, and two
// reproduce calls must agree step for step.
func TestReproduceWalkIsDeterministic(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/c") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()
	srv, cp, closeCP := newCPServer(t)
	defer closeCP()

	// a branches to b or c with equal weight; only /c fails.
	spec := specFor(sut.URL, 8)
	spec.Graph = domain.ScenarioGraph{
		ID: "g",
		Nodes: []domain.Node{
			{ID: "a", APITemplateID: "ta"}, {ID: "b", APITemplateID: "tb"}, {ID: "c", APITemplateID: "tc"},
		},
		Edges: []domain.Edge{
			{From: "a", To: "b", Weight: 1}, {From: "a", To: "c", Weight: 1},
		},
	}
	spec.Templates["tc"] = domain.APITemplate{Method: "GET", Path: "/c"}
	rep := runToReport(t, cp.URL, spec)

	f := findingWithRef(rep, domain.FindingContract, "c")
	if f == nil || f.Evidence == nil || len(f.Evidence.Sessions) == 0 {
		t.Fatalf("no contract finding with evidence for c: %+v", rep.Findings)
	}
	wantPath := f.Evidence.Sessions[0].Path

	first, err := srv.ReproduceFinding(context.Background(), rep.Run.ID,
		ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "c"})
	if err != nil {
		t.Fatalf("ReproduceFinding: %v", err)
	}
	second, err := srv.ReproduceFinding(context.Background(), rep.Run.ID,
		ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "c"})
	if err != nil {
		t.Fatalf("ReproduceFinding (second): %v", err)
	}
	for _, res := range []ReproduceResult{first, second} {
		for i, at := range res.Attempts {
			if got := stepNodes(at); !equalIDs(got, wantPath) {
				t.Errorf("attempt %d walked %v, want the evidence path %v", i+1, got, wantPath)
			}
		}
	}
}

// TestReproduceEnforcesSafetyPolicy: the replay rebuilds the guard from the
// engine's current spec, so a target that is no longer allowlisted — or has
// been reclassified prod-locked — is refused before any traffic is sent.
func TestReproduceEnforcesSafetyPolicy(t *testing.T) {
	sut := sutFailB()
	defer sut.Close()
	srv, cp, closeCP := newCPServer(t)
	defer closeCP()

	rep := runToReport(t, cp.URL, specFor(sut.URL, 3))
	req := ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "b"}

	mutateSpec := func(f func(*RunSpec)) {
		srv.mu.Lock()
		spec := srv.specs[rep.Run.ExperimentID]
		f(&spec)
		srv.specs[rep.Run.ExperimentID] = spec
		srv.mu.Unlock()
	}

	// Allowlist no longer matches the target host.
	original := specFor(sut.URL, 3).TargetEnv.Allowlist
	mutateSpec(func(s *RunSpec) { s.TargetEnv.Allowlist = []string{"elsewhere.example"} })
	var ge *guardError
	if _, err := srv.ReproduceFinding(context.Background(), rep.Run.ID, req); !errors.As(err, &ge) {
		t.Errorf("off-allowlist reproduce err = %v, want a guard rejection", err)
	}

	// Target reclassified prod-locked: refused without an explicit unlock.
	mutateSpec(func(s *RunSpec) { s.TargetEnv.Allowlist = original; s.TargetEnv.EnvClass = domain.EnvProdLocked })
	if _, err := srv.ReproduceFinding(context.Background(), rep.Run.ID, req); !errors.As(err, &ge) {
		t.Errorf("prod-locked reproduce err = %v, want a guard rejection", err)
	} else if !strings.Contains(err.Error(), "prod-locked") {
		t.Errorf("prod-locked rejection should say why: %v", err)
	}
}

// TestReproduceHTTPEndpoint drives POST /runs/{id}/reproduce over the wire and
// pins the JSON contract the CLI consumes (vu/seed/userIndex coordinates,
// attempts, rootCauseClass).
func TestReproduceHTTPEndpoint(t *testing.T) {
	sut := sutFailB()
	defer sut.Close()
	_, cp, closeCP := newCPServer(t)
	defer closeCP()

	rep := runToReport(t, cp.URL, specFor(sut.URL, 4))

	resp := postJSON(t, cp.URL+"/runs/"+string(rep.Run.ID)+"/reproduce",
		map[string]any{"category": "contract", "evidenceRef": "b", "attempts": 2})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reproduce status = %d", resp.StatusCode)
	}
	var res ReproduceResult
	decode(t, resp, &res)
	if res.RootCauseClass != domain.RootCauseFunctional || len(res.Attempts) != 2 {
		t.Errorf("got class=%q attempts=%d, want functional/2", res.RootCauseClass, len(res.Attempts))
	}
	if res.Session.SessionID == "" {
		t.Error("result names no session; the operator cannot grep target logs without it")
	}
	if res.Session.Seed != res.RunSeed+res.Session.UserIndex {
		t.Errorf("coordinates inconsistent: seed %d != run seed %d + index %d", res.Session.Seed, res.RunSeed, res.Session.UserIndex)
	}
}

// TestReproduceErrorCases covers the refusal paths: unknown run, unknown
// finding, a finding with no reproduce coordinates, and a run whose spec the
// engine no longer holds.
func TestReproduceErrorCases(t *testing.T) {
	sut := sutFailB()
	defer sut.Close()
	srv, cp, closeCP := newCPServer(t)
	defer closeCP()
	ctx := context.Background()

	if _, err := srv.ReproduceFinding(ctx, "nope", ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "b"}); !errors.Is(err, errRunNotFound) {
		t.Errorf("unknown run err = %v, want errRunNotFound", err)
	}

	rep := runToReport(t, cp.URL, specFor(sut.URL, 3))
	if _, err := srv.ReproduceFinding(ctx, rep.Run.ID, ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "zzz"}); !errors.Is(err, errFindingNotFound) {
		t.Errorf("unknown finding err = %v, want errFindingNotFound", err)
	}
	if _, err := srv.ReproduceFinding(ctx, rep.Run.ID, ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "b", Attempts: -1}); err == nil {
		t.Error("negative attempts should be rejected")
	}

	// A finding stripped of evidence has no coordinates to replay.
	srv.mu.Lock()
	rs := srv.runs[rep.Run.ID]
	srv.mu.Unlock()
	rs.mu.Lock()
	for i := range rs.findings {
		rs.findings[i].Evidence = nil
	}
	rs.mu.Unlock()
	if _, err := srv.ReproduceFinding(ctx, rep.Run.ID, ReproduceRequest{Category: domain.FindingContract, EvidenceRef: "b"}); !errors.Is(err, errNotReproducible) {
		t.Errorf("evidence-less finding err = %v, want errNotReproducible", err)
	}

	// A run whose spec was evicted (or lost to a restart) cannot be replayed:
	// the spec lives in engine memory only.
	srv.mu.Lock()
	delete(srv.specs, rep.Run.ExperimentID)
	srv.mu.Unlock()
	rs.mu.Lock()
	rs.findings[0].Evidence = &domain.FindingEvidence{Sessions: []domain.EvidenceSession{{SessionID: "u0", Seed: 1}}}
	cat, ref := rs.findings[0].Category, rs.findings[0].EvidenceRef
	rs.mu.Unlock()
	if _, err := srv.ReproduceFinding(ctx, rep.Run.ID, ReproduceRequest{Category: cat, EvidenceRef: ref}); !errors.Is(err, errSpecUnavailable) {
		t.Errorf("missing spec err = %v, want errSpecUnavailable", err)
	}
}

// TestAnnotateRootCauseConcurrent guards the read-modify-write atomicity of
// annotateRootCause: two goroutines annotate different findings of the same
// run concurrently. Without s.annotateMu the second SaveFindings would be
// built from a stale read that does not yet contain the first finding's class,
// silently overwriting it. The test checks that both RootCauseClass values
// survive in the store (the system of record) when the race detector is on.
func TestAnnotateRootCauseConcurrent(t *testing.T) {
	// SUT that fails /b and /c unconditionally, producing two distinct contract
	// findings. The three-node graph a->b and a->c gives each finding a unique
	// EvidenceRef the annotation path keyed on.
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/b"),
			strings.HasSuffix(r.URL.Path, "/c"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer sut.Close()

	srv, cp, closeCP := newCPServer(t)
	defer closeCP()

	spec := specFor(sut.URL, 8)
	spec.Graph = domain.ScenarioGraph{
		ID: "g",
		Nodes: []domain.Node{
			{ID: "a", APITemplateID: "ta"},
			{ID: "b", APITemplateID: "tb"},
			{ID: "c", APITemplateID: "tc"},
		},
		Edges: []domain.Edge{
			{From: "a", To: "b", Weight: 1},
			{From: "a", To: "c", Weight: 1},
		},
	}
	spec.Templates["tc"] = domain.APITemplate{Method: "GET", Path: "/c"}

	rep := runToReport(t, cp.URL, spec)

	// We need a finding for both "b" and "c" with evidence sessions to replay.
	fb := findingWithRef(rep, domain.FindingContract, "b")
	fc := findingWithRef(rep, domain.FindingContract, "c")
	if fb == nil || fb.Evidence == nil || len(fb.Evidence.Sessions) == 0 {
		t.Skip("no contract finding with evidence for b (probabilistic graph: re-run)")
	}
	if fc == nil || fc.Evidence == nil || len(fc.Evidence.Sessions) == 0 {
		t.Skip("no contract finding with evidence for c (probabilistic graph: re-run)")
	}

	// Annotate both findings concurrently. The two goroutines call annotateRootCause
	// simultaneously so the race detector can observe any unsynchronized access, and
	// the store read-modify-write for "b" and "c" must not produce a lost update.
	classB := domain.RootCauseFunctional
	classC := domain.RootCauseFunctional

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		srv.annotateRootCause(rep.Run.ID, domain.FindingContract, "b", classB)
	}()
	go func() {
		defer wg.Done()
		srv.annotateRootCause(rep.Run.ID, domain.FindingContract, "c", classC)
	}()
	wg.Wait()

	stored, err := srv.store.Findings(rep.Run.ID)
	if err != nil {
		t.Fatalf("store findings: %v", err)
	}
	if f := findingIn(stored, domain.FindingContract, "b"); f == nil || f.RootCauseClass != classB {
		t.Errorf("stored finding for b: got %+v, want RootCauseClass=%q", f, classB)
	}
	if f := findingIn(stored, domain.FindingContract, "c"); f == nil || f.RootCauseClass != classC {
		t.Errorf("stored finding for c: got %+v, want RootCauseClass=%q", f, classC)
	}
}

// --- helpers -----------------------------------------------------------------

func findingIn(fs []domain.Finding, cat domain.FindingCategory, ref string) *domain.Finding {
	for i := range fs {
		if fs[i].Category == cat && fs[i].EvidenceRef == ref {
			return &fs[i]
		}
	}
	return nil
}

func mustFindings(t *testing.T, srv *Server, runID domain.ID) []domain.Finding {
	t.Helper()
	fs, err := srv.store.Findings(runID)
	if err != nil {
		t.Fatalf("store findings: %v", err)
	}
	return fs
}

func stepNodes(at ReproduceAttempt) []domain.ID {
	out := make([]domain.ID, 0, len(at.Steps))
	for _, st := range at.Steps {
		out = append(out, st.Node)
	}
	return out
}

func equalIDs(a, b []domain.ID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
