package api

import (
	"net/http"
	"net/http/httptest"
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
