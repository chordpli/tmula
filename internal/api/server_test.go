package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
)

func sutOK() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func specFor(sutURL string, users int) RunSpec {
	vus := make([]load.VirtualUser, users)
	for i := range vus {
		vus[i] = load.VirtualUser{ID: fmt.Sprintf("u%d", i)}
	}
	return RunSpec{
		Experiment: domain.Experiment{
			Name: "smoke", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: users, DeviationRate: 0, AuthStrategy: domain.CredPool},
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

func newCP(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	cp := httptest.NewServer(srv.Handler())
	return cp, cp.Close
}

func postJSON(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(v)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestExperimentLifecycle(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	// create
	resp := postJSON(t, cp.URL+"/experiments", specFor(sut.URL, 10))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct{ ID string }
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("no experiment id returned")
	}

	// get
	gr, err := http.Get(cp.URL + "/experiments/" + created.ID)
	if err != nil || gr.StatusCode != http.StatusOK {
		t.Fatalf("get experiment: %v status=%v", err, gr.StatusCode)
	}
	gr.Body.Close()

	// run
	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d", resp.StatusCode)
	}
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	// poll report until completed
	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 3*time.Second)
	if report.Stats.Total != 20 { // 10 users * 2 nodes
		t.Errorf("stats.Total = %d, want 20", report.Stats.Total)
	}
	if report.Stats.Errors != 0 {
		t.Errorf("stats.Errors = %d, want 0", report.Stats.Errors)
	}
}

func waitForStatus(t *testing.T, reportURL string, want domain.RunStatus, timeout time.Duration) Report {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		resp, err := http.Get(reportURL)
		if err != nil {
			t.Fatalf("get report: %v", err)
		}
		var rep Report
		decode(t, resp, &rep)
		if rep.Run.Status == want {
			return rep
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for status %q, last = %q", want, rep.Run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunRejectsHostNotInAllowlist(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 1)
	spec.TargetEnv.Allowlist = []string{"example.com"} // SUT host not allowed
	resp := postJSON(t, cp.URL+"/experiments", spec)
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("run with disallowed host = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCreateRejectsInvalidSpec(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()
	// Missing target env / graph / users.
	resp := postJSON(t, cp.URL+"/experiments", RunSpec{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid spec = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestKillRun(t *testing.T) {
	// Slow SUT so the run is still in flight when we kill it.
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(150 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	resp := postJSON(t, cp.URL+"/experiments", specFor(sut.URL, 30))
	var created struct{ ID string }
	decode(t, resp, &created)
	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	time.Sleep(20 * time.Millisecond) // let it start
	kr := postJSON(t, cp.URL+"/runs/"+run.RunID+"/kill?reason=test", nil)
	if kr.StatusCode != http.StatusOK {
		t.Fatalf("kill status = %d", kr.StatusCode)
	}
	kr.Body.Close()

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunKilled, 3*time.Second)
	if report.Run.KillReason == "" {
		t.Error("expected a kill reason")
	}
}

func TestSSEStream(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cp.URL+"/runs/"+run.RunID+"/stream", nil)
	sr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer sr.Body.Close()

	sc := bufio.NewScanner(sr.Body)
	sawCompleted := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, string(domain.RunCompleted)) {
			sawCompleted = true
			break
		}
	}
	if !sawCompleted {
		t.Fatal("SSE stream never reported a completed run")
	}
}
