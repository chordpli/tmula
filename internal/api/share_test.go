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
	"github.com/chordpli/tmula/internal/obs"
)

func TestShareLifecycle(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	resp := postJSON(t, cp.URL+"/experiments", specFor(sut.URL, 5))
	var created struct{ ID string }
	decode(t, resp, &created)
	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)
	waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 3*time.Second)

	// Operator issues a read-only share token.
	sresp := postJSON(t, cp.URL+"/runs/"+run.RunID+"/share", nil)
	if sresp.StatusCode != http.StatusCreated {
		t.Fatalf("share status = %d", sresp.StatusCode)
	}
	var share struct{ Token, URL, Scope string }
	decode(t, sresp, &share)
	if share.Token == "" || share.Scope != string(domain.RoleViewer) {
		t.Fatalf("unexpected share payload %+v", share)
	}

	// Viewer fetches the masked, read-only report via the token.
	gr, err := http.Get(cp.URL + "/reports/shared/" + share.Token)
	if err != nil || gr.StatusCode != http.StatusOK {
		t.Fatalf("shared report: %v status=%v", err, gr.StatusCode)
	}
	var rep Report
	decode(t, gr, &rep)
	if rep.Stats.Total != 10 { // 5 users * 2 nodes
		t.Errorf("shared report stats.Total = %d, want 10", rep.Stats.Total)
	}
}

// TestSharedReportHidesInternalKillReason: a run that failed with an internal
// killReason (carrying a worker address) must not leak that text through a share
// token, while the operator report keeps the full detail. The masker redacts PII
// by field name but not killReason, so the shared path scrubs it explicitly.
func TestSharedReportHidesInternalKillReason(t *testing.T) {
	const internalReason = `dial worker "10.0.0.5:7000": connection refused`

	s := NewServer(load.NewRESTAdapter(time.Second))
	now := time.Unix(2000, 0)
	s.now = func() time.Time { return now }
	s.runs["r1"] = &runState{
		exec: domain.RunExecution{
			ID: "r1", Status: domain.RunFailed, KillReason: internalReason,
		},
		collector: obs.NewCollector(),
		done:      make(chan struct{}),
	}
	s.shares["tok"] = shareEntry{runID: "r1"}

	// Shared path: the internal text must be gone.
	shReq := httptest.NewRequest(http.MethodGet, "/reports/shared/tok", nil)
	shRR := httptest.NewRecorder()
	s.Handler().ServeHTTP(shRR, shReq)
	if shRR.Code != http.StatusOK {
		t.Fatalf("shared report = %d, want 200", shRR.Code)
	}
	shBody := shRR.Body.String()
	if strings.Contains(shBody, "10.0.0.5") || strings.Contains(shBody, internalReason) {
		t.Errorf("shared report leaks internal killReason: %s", shBody)
	}

	// Operator path: the full detail must remain.
	opReq := httptest.NewRequest(http.MethodGet, "/runs/r1/report", nil)
	opRR := httptest.NewRecorder()
	s.Handler().ServeHTTP(opRR, opReq)
	if opRR.Code != http.StatusOK {
		t.Fatalf("operator report = %d, want 200", opRR.Code)
	}
	// The worker address survives JSON string escaping intact, so match on it
	// rather than the whole reason (whose embedded quotes get escaped).
	opBody, _ := io.ReadAll(opRR.Body)
	if !strings.Contains(string(opBody), "10.0.0.5:7000") {
		t.Errorf("operator report should keep full killReason, got: %s", opBody)
	}
}

func TestShareInvalidToken(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()
	gr, _ := http.Get(cp.URL + "/reports/shared/deadbeef")
	if gr.StatusCode != http.StatusNotFound {
		t.Fatalf("invalid token = %d, want 404", gr.StatusCode)
	}
}

func TestShareUnknownRun(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()
	resp := postJSON(t, cp.URL+"/runs/nope/share", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("share of unknown run = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestShareExpiredReadDeletes: reading an expired share returns 410 AND removes
// the token, so a one-shot link cannot linger in memory.
func TestShareExpiredReadDeletes(t *testing.T) {
	s := NewServer(load.NewRESTAdapter(time.Second))
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	s.runs["r1"] = &runState{
		exec:      domain.RunExecution{ID: "r1", Status: domain.RunCompleted},
		collector: obs.NewCollector(),
		done:      make(chan struct{}),
	}
	exp := now.Add(time.Second)
	s.registerShareLocked("tok", shareEntry{runID: "r1", expiresAt: &exp})
	now = now.Add(2 * time.Second) // past expiry

	req := httptest.NewRequest(http.MethodGet, "/reports/shared/tok", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusGone {
		t.Fatalf("expired share = %d, want 410", rr.Code)
	}
	s.mu.Lock()
	_, stillThere := s.shares["tok"]
	s.mu.Unlock()
	if stillThere {
		t.Error("expired share token should be deleted on read")
	}
}

// TestShareCapEvictsOldest: exceeding the share cap evicts the oldest tokens,
// preferring expired ones, while keeping the most recent.
func TestShareCapEvictsOldest(t *testing.T) {
	s := NewServer(load.NewRESTAdapter(time.Second))
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }

	past := now.Add(-time.Second)
	s.mu.Lock()
	// Oldest token is already expired; it should be reclaimed first.
	s.registerShareLocked("expired", shareEntry{runID: "r", expiresAt: &past})
	s.registerShareLocked("old", shareEntry{runID: "r"})
	s.registerShareLocked("new", shareEntry{runID: "r"})
	s.enforceShareCapLocked(2)
	defer s.mu.Unlock()

	if len(s.shares) > 2 {
		t.Fatalf("shares = %d, want <= 2", len(s.shares))
	}
	if _, ok := s.shares["expired"]; ok {
		t.Error("expired token should be evicted first")
	}
	if _, ok := s.shares["new"]; !ok {
		t.Error("most recent token should be retained")
	}
}

func TestShareExpired(t *testing.T) {
	s := NewServer(load.NewRESTAdapter(time.Second))
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	s.runs["r1"] = &runState{
		exec:      domain.RunExecution{ID: "r1", Status: domain.RunCompleted},
		collector: obs.NewCollector(),
		done:      make(chan struct{}),
	}
	exp := now.Add(time.Second)
	s.shares["tok"] = shareEntry{runID: "r1", expiresAt: &exp}

	now = now.Add(2 * time.Second) // advance past expiry

	req := httptest.NewRequest(http.MethodGet, "/reports/shared/tok", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusGone {
		t.Fatalf("expired share = %d, want 410", rr.Code)
	}
}
