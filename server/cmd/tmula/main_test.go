package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

func TestRunVersion(t *testing.T) {
	if err := run([]string{"--version"}); err != nil {
		t.Fatalf("run --version returned error: %v", err)
	}
}

func TestRunInvalidRole(t *testing.T) {
	if err := run([]string{"--role", "bogus"}); err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
}

// TestSetupStoreLocalInMemory: the zero-config local role gets an in-memory store
// and a no-op closer that touches no disk.
func TestSetupStoreLocalInMemory(t *testing.T) {
	st, closeStore, err := setupStore(domain.RoleLocal, "", "")
	if err != nil {
		t.Fatalf("setupStore: %v", err)
	}
	if st == nil {
		t.Fatal("store should not be nil")
	}
	closeStore() // must be safe and a no-op (no path configured)
}

// TestSetupStoreMasterFallsBackToMemory: master without a DSN must not fail; it
// degrades to an in-memory store so a misconfigured master still serves.
func TestSetupStoreMasterFallsBackToMemory(t *testing.T) {
	st, closeStore, err := setupStore(domain.RoleMaster, "", "")
	if err != nil {
		t.Fatalf("setupStore master/no-dsn: %v", err)
	}
	if st == nil {
		t.Fatal("store should not be nil on fallback")
	}
	closeStore()
}

// TestSetupStoreLocalSnapshotRoundTrip: with a --store path, the closer writes a
// snapshot and a fresh setup loads it, so a local restart keeps run history. A
// missing file on first start is not an error.
func TestSetupStoreLocalSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")

	first, closeFirst, err := setupStore(domain.RoleLocal, path, "")
	if err != nil {
		t.Fatalf("setupStore (first, missing file): %v", err)
	}
	if err := first.SaveRun(domain.RunExecution{ID: "r1", Status: domain.RunCompleted}); err != nil {
		t.Fatalf("save run: %v", err)
	}
	closeFirst() // writes the snapshot

	second, closeSecond, err := setupStore(domain.RoleLocal, path, "")
	if err != nil {
		t.Fatalf("setupStore (second, existing file): %v", err)
	}
	defer closeSecond()
	if r, err := second.GetRun("r1"); err != nil || r.Status != domain.RunCompleted {
		t.Errorf("reloaded run = %+v, %v; history did not survive restart", r, err)
	}
}

// TestEngineImportStatsWiredE2E is the regression guard for the production
// import wiring: a review found WithImporterStats referenced only by the api
// package's own tests while both real servers (serve and `tmula demo`) still
// registered the legacy importer, so POST /api/import never returned stats and
// the web coverage panel stayed invisible. Both servers now assemble their
// engine through newEngineServer, so POSTing a real access log to that exact
// surface proves the coverage report ships in production, not just in stubs.
func TestEngineImportStatsWiredE2E(t *testing.T) {
	_, handler := newEngineServer(domain.RoleLocal)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	logData := sampleAccessLog + "definitely not an access log line\n"
	resp, err := http.Post(ts.URL+"/api/import", "text/plain", strings.NewReader(logData))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var got struct {
		Start string `json:"start"`
		Stats *struct {
			Format         string `json:"format"`
			Requests       int    `json:"requests"`
			Skipped        int    `json:"skipped"`
			Sessions       int    `json:"sessions"`
			Clients        int    `json:"clients"`
			SkippedSamples []struct {
				Line   int    `json:"line"`
				Text   string `json:"text"`
				Reason string `json:"reason"`
			} `json:"skippedSamples"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Start == "" {
		t.Error("imported spec has no start node")
	}
	if got.Stats == nil {
		t.Fatal("stats missing: the production server is not wired with the stats-aware importer")
	}
	if got.Stats.Requests != 3 || got.Stats.Sessions != 2 || got.Stats.Clients != 2 {
		t.Errorf("stats = %+v, want requests/sessions/clients = 3/2/2", got.Stats)
	}
	if got.Stats.Format != "combined" {
		t.Errorf("stats.format = %q, want combined", got.Stats.Format)
	}
	if got.Stats.Skipped != 1 || len(got.Stats.SkippedSamples) != 1 {
		t.Fatalf("skipped = %d with %d sample(s), want the garbage line counted and sampled",
			got.Stats.Skipped, len(got.Stats.SkippedSamples))
	}
	if s := got.Stats.SkippedSamples[0]; s.Line != 4 || s.Reason == "" {
		t.Errorf("skipped sample = %+v, want line 4 with a reason", s)
	}
}

func TestResolveDSN(t *testing.T) {
	if got := resolveDSN("flag-dsn"); got != "flag-dsn" {
		t.Errorf("flag should win: got %q", got)
	}
	t.Setenv("TMULA_DB_DSN", "env-dsn")
	if got := resolveDSN(""); got != "env-dsn" {
		t.Errorf("env should be used when flag blank: got %q", got)
	}
	if got := resolveDSN("  flag  "); got != "  flag  " {
		t.Errorf("non-blank flag should win verbatim: got %q", got)
	}
}
