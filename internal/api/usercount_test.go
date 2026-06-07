package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
)

// specForCount builds a closed spec that ships NO user array, only a pool size as
// a count — the shape the web UI now sends so a huge closed run fits in a small
// request body. It reuses specFor's graph/templates/env and keeps the experiment's
// VirtualUserCount positive so only the new pool-from-count path is exercised.
func specForCount(sutURL string, count int) RunSpec {
	s := specFor(sutURL, count)
	s.Users = nil
	s.UserCount = count
	return s
}

// TestPoolSizeAndClosedUsers covers the pure pool helpers: an explicit Users list
// always wins, and a count-only spec synthesizes a stable u0..u{N-1} pool.
func TestPoolSizeAndClosedUsers(t *testing.T) {
	// Explicit pool wins over any count: PoolSize is its length, ClosedUsers
	// returns it verbatim.
	explicit := RunSpec{Users: []load.VirtualUser{{ID: "a"}, {ID: "b"}}, UserCount: 99}
	if got := explicit.PoolSize(); got != 2 {
		t.Errorf("PoolSize with explicit users = %d, want 2", got)
	}
	if got := explicit.ClosedUsers(); len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("ClosedUsers with explicit pool = %+v, want the sent pool", got)
	}

	// Count-only: PoolSize is the count, ClosedUsers synthesizes u0..uN-1.
	counted := RunSpec{UserCount: 3}
	if got := counted.PoolSize(); got != 3 {
		t.Errorf("PoolSize from count = %d, want 3", got)
	}
	got := counted.ClosedUsers()
	if len(got) != 3 {
		t.Fatalf("ClosedUsers from count = %d users, want 3", len(got))
	}
	for i, want := range []string{"u0", "u1", "u2"} {
		if got[i].ID != want {
			t.Errorf("ClosedUsers[%d].ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

// TestClosedRunSynthesizesPoolFromCount runs a closed experiment requested with
// only a count (no user array) end-to-end and asserts the server synthesized the
// full pool — proving a large closed run no longer needs one object per user in
// the body.
func TestClosedRunSynthesizesPoolFromCount(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	const users = 10
	const nodes = 2 // graph "a" -> "b"
	resp := postJSON(t, cp.URL+"/experiments", specForCount(sut.URL, users))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, want 202", resp.StatusCode)
	}
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 3*time.Second)
	if report.Stats.Total != users*nodes {
		t.Errorf("stats.Total = %d, want %d (synthesized users * nodes)", report.Stats.Total, users*nodes)
	}
	if report.Stats.Errors != 0 {
		t.Errorf("stats.Errors = %d, want 0", report.Stats.Errors)
	}
}

// TestLocalPoolCapRejectsHugeCount bounds the in-process pool a count-only closed
// run may synthesize: at the cap the create is accepted, above it (with no workers)
// it is rejected so a tiny request cannot ask the control plane for an unbounded
// pool, and above it with workers set it is accepted — the cap guards the local
// path only; the worker fan-out is the path built for that scale.
func TestLocalPoolCapRejectsHugeCount(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()
	sut := sutOK()
	defer sut.Close()

	// At the cap: accepted (create only stores the spec; it does not synthesize).
	resp := postJSON(t, cp.URL+"/experiments", specForCount(sut.URL, maxLocalPoolUsers))
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("count at cap = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	// Above the cap with no workers (in-process): rejected at create.
	resp = postJSON(t, cp.URL+"/experiments", specForCount(sut.URL, maxLocalPoolUsers+1))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("count over cap (local) = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Above the cap but distributable (workers set): accepted — workers are never
	// dialed at create time, so the bogus address is fine here.
	spec := specForCount(sut.URL, maxLocalPoolUsers+1)
	spec.Workers = []string{"127.0.0.1:65535"}
	resp = postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("count over cap (distributed) = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestClosedRunFromCountAcrossWorkers fans a count-only closed run across two real
// gRPC workers. The control plane must drive the fan-out from the pool count (the
// distributed path never ships the array — workers synthesize their own shard), so
// every request must still reach the SUT.
func TestClosedRunFromCountAcrossWorkers(t *testing.T) {
	var hits int32
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	w1, stop1 := startWorker(t)
	defer stop1()
	w2, stop2 := startWorker(t)
	defer stop2()

	cp, closeCP := newCP(t)
	defer closeCP()

	const users = 10
	const nodes = 2
	spec := specForCount(sut.URL, users)
	spec.Workers = []string{w1, w2}

	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, want 202", resp.StatusCode)
	}
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)
	if report.Stats.Total != users*nodes {
		t.Errorf("stats.Total = %d, want %d (users*nodes)", report.Stats.Total, users*nodes)
	}
	if got := atomic.LoadInt32(&hits); int(got) != users*nodes {
		t.Errorf("SUT hits = %d, want %d (count drove the fan-out)", got, users*nodes)
	}
}
