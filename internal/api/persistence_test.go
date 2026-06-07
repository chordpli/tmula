package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/store"
)

// newCPWithStore is like newCP but injects an explicit store so a test can assert
// what was persisted and drive the serve-from-store path. It returns the server
// (for cache surgery), its HTTP test server, the store, and a cleanup func.
func newCPWithStore(t *testing.T) (*Server, *httptest.Server, store.Store, func()) {
	t.Helper()
	st := store.NewMemStore()
	srv := NewServer(load.NewRESTAdapter(2*time.Second), store2Opt(st))
	cp := httptest.NewServer(srv.Handler())
	return srv, cp, st, cp.Close
}

// store2Opt wraps WithStore so the helper reads clearly at the call site.
func store2Opt(st store.Store) Option { return WithStore(st) }

// evict removes a run from the in-memory cache (and its order entry), simulating
// retention eviction or a process restart while the store keeps the report.
func evict(s *Server, id domain.ID) {
	s.mu.Lock()
	delete(s.runs, id)
	delete(s.specs, id)
	kept := s.runOrder[:0:0]
	for _, rid := range s.runOrder {
		if rid != id {
			kept = append(kept, rid)
		}
	}
	s.runOrder = kept
	s.mu.Unlock()
}

// TestReportServedFromStoreAfterEviction drives the core durability guarantee:
// a completed run, once dropped from the in-memory cache, is still reported from
// the store with the same stats the live report had.
func TestReportServedFromStoreAfterEviction(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	srv, cp, _, closeCP := newCPWithStore(t)
	defer closeCP()

	runID := runOnce(t, cp.URL, sut.URL, 5)
	// Capture the live report before eviction so we can compare.
	live := waitForStatus(t, cp.URL+"/runs/"+runID+"/report", domain.RunCompleted, 3*time.Second)
	if live.Stats.Total != 10 { // 5 users * 2 nodes
		t.Fatalf("live stats.Total = %d, want 10", live.Stats.Total)
	}

	evict(srv, domain.ID(runID))

	// The live cache no longer has it; the report must come from the store.
	srv.mu.Lock()
	_, stillCached := srv.runs[domain.ID(runID)]
	srv.mu.Unlock()
	if stillCached {
		t.Fatal("run should have been evicted from the cache")
	}

	resp, err := http.Get(cp.URL + "/runs/" + runID + "/report")
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	var rebuilt Report
	decode(t, resp, &rebuilt)
	if rebuilt.Run.Status != domain.RunCompleted {
		t.Errorf("rebuilt status = %q, want completed", rebuilt.Run.Status)
	}
	if rebuilt.Stats.Total != live.Stats.Total {
		t.Errorf("rebuilt stats.Total = %d, want %d (live)", rebuilt.Stats.Total, live.Stats.Total)
	}
	if rebuilt.Run.ID != domain.ID(runID) {
		t.Errorf("rebuilt run id = %q, want %q", rebuilt.Run.ID, runID)
	}
}

// TestReportHTMLServedFromStoreAfterEviction is the HTML twin: report.html must
// render from the store once the live state is gone, still naming the run.
func TestReportHTMLServedFromStoreAfterEviction(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	srv, cp, _, closeCP := newCPWithStore(t)
	defer closeCP()

	runID := runOnce(t, cp.URL, sut.URL, 4)
	waitForStatus(t, cp.URL+"/runs/"+runID+"/report", domain.RunCompleted, 3*time.Second)
	evict(srv, domain.ID(runID))

	resp, err := http.Get(cp.URL + "/runs/" + runID + "/report.html")
	if err != nil {
		t.Fatalf("get report.html: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report.html after eviction = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), runID) {
		t.Errorf("report html (from store) does not contain run id %q", runID)
	}
	if !strings.Contains(string(body), "<!doctype html>") {
		t.Error("report html (from store) is not a full HTML document")
	}
}

// TestReportUnknownRunStill404 confirms the fallback does not turn a genuinely
// unknown run into a 200: absent from both cache and store stays a 404.
func TestReportUnknownRunStill404(t *testing.T) {
	_, cp, _, closeCP := newCPWithStore(t)
	defer closeCP()

	resp, err := http.Get(cp.URL + "/runs/never-existed/report")
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run report = %d, want 404", resp.StatusCode)
	}
}

// TestSharedReportServedFromStoreAfterEviction: a share link keeps resolving
// after the run is evicted, because the shared path also rebuilds from the store.
func TestSharedReportServedFromStoreAfterEviction(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	srv, cp, _, closeCP := newCPWithStore(t)
	defer closeCP()

	runID := runOnce(t, cp.URL, sut.URL, 5)
	waitForStatus(t, cp.URL+"/runs/"+runID+"/report", domain.RunCompleted, 3*time.Second)

	// Issue a share token while the run is live.
	sresp := postJSON(t, cp.URL+"/runs/"+runID+"/share", nil)
	var share struct{ Token, URL, Scope string }
	decode(t, sresp, &share)
	if share.Token == "" {
		t.Fatal("no share token issued")
	}

	evict(srv, domain.ID(runID))

	gr, err := http.Get(cp.URL + "/reports/shared/" + share.Token)
	if err != nil {
		t.Fatalf("shared report: %v", err)
	}
	if gr.StatusCode != http.StatusOK {
		t.Fatalf("shared report after eviction = %d, want 200", gr.StatusCode)
	}
	var rep Report
	decode(t, gr, &rep)
	if rep.Stats.Total != 10 {
		t.Errorf("shared (from store) stats.Total = %d, want 10", rep.Stats.Total)
	}
}

// TestRunCapEvictionKeepsPersistedReport ties the retention bound to the store:
// after the cap evicts the oldest terminal runs from memory, their reports are
// still served from the store. This is the end-to-end form of requirement (4).
func TestRunCapEvictionKeepsPersistedReport(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	srv, cp, st, closeCP := newCPWithStore(t)
	defer closeCP()

	// Run, complete, and confirm it persisted.
	runID := runOnce(t, cp.URL, sut.URL, 3)
	waitForStatus(t, cp.URL+"/runs/"+runID+"/report", domain.RunCompleted, 3*time.Second)
	if _, err := st.GetRun(domain.ID(runID)); err != nil {
		t.Fatalf("run not persisted on finalize: %v", err)
	}

	// A second completed run, so a cap of 1 must evict the older terminal run.
	newer := runOnce(t, cp.URL, sut.URL, 2)
	waitForStatus(t, cp.URL+"/runs/"+newer+"/report", domain.RunCompleted, 3*time.Second)

	// Force the retention bound to drop the oldest terminal run from memory.
	srv.mu.Lock()
	srv.enforceRunCapLocked(1)
	_, cached := srv.runs[domain.ID(runID)]
	srv.mu.Unlock()
	if cached {
		t.Fatal("oldest terminal run should have been evicted by the cap")
	}

	// The report is still available, served from the store.
	resp, err := http.Get(cp.URL + "/runs/" + runID + "/report")
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	var rep Report
	decode(t, resp, &rep)
	if rep.Run.ID != domain.ID(runID) || rep.Run.Status != domain.RunCompleted {
		t.Errorf("report after cap eviction = %+v, want completed run %q", rep.Run, runID)
	}
}

// TestDefaultStoreIsInMemory asserts NewServer wires an in-process store by
// default (no WithStore), so finalized runs persist and a default-built server
// still serves a report from the store after eviction.
func TestDefaultStoreIsInMemory(t *testing.T) {
	sut := sutOK()
	defer sut.Close()

	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	if srv.store == nil {
		t.Fatal("NewServer should default to a non-nil store")
	}
	cp := httptest.NewServer(srv.Handler())
	defer cp.Close()

	runID := runOnce(t, cp.URL, sut.URL, 3)
	waitForStatus(t, cp.URL+"/runs/"+runID+"/report", domain.RunCompleted, 3*time.Second)
	evict(srv, domain.ID(runID))

	resp, err := http.Get(cp.URL + "/runs/" + runID + "/report")
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	var rep Report
	decode(t, resp, &rep)
	if rep.Run.Status != domain.RunCompleted {
		t.Errorf("default-store report after eviction = %q, want completed", rep.Run.Status)
	}
}
