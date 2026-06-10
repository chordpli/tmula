package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

func TestHeatAggRecordSnapshot(t *testing.T) {
	g := domain.ScenarioGraph{
		Nodes: []domain.Node{{ID: "a"}, {ID: "b"}},
		Edges: []domain.Edge{{From: "a", To: "b", Weight: 1}},
	}
	h := newHeatAgg(g)
	h.record(load.StepEvent{From: "", To: "a", OK: true}) // entry
	h.record(load.StepEvent{From: "a", To: "b", OK: true})
	h.record(load.StepEvent{From: "a", To: "b", OK: false}) // one error
	h.record(load.StepEvent{From: "x", To: "y", OK: true})  // unknown edge → ignored

	var entryReq, abReq, abErr int64
	for _, e := range h.snapshot() {
		switch {
		case e.From == "" && e.To == "a":
			entryReq = e.Requests
		case e.From == "a" && e.To == "b":
			abReq, abErr = e.Requests, e.Errors
		case e.From == "x":
			t.Errorf("unknown edge should be ignored, got %+v", e)
		}
	}
	if entryReq != 1 {
		t.Errorf("entry ->a requests = %d, want 1", entryReq)
	}
	if abReq != 2 || abErr != 1 {
		t.Errorf("a->b = {req:%d err:%d}, want {2 1}", abReq, abErr)
	}
}

// TestHeatmapStreamsEdges runs a traced experiment and reads per-edge aggregates
// from the /heatmap SSE stream.
func TestHeatmapStreamsEdges(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 3) // graph a -> b
	spec.Trace = true

	resp := postJSON(t, cp.URL+"/experiments", spec)
	var created struct{ ID string }
	decode(t, resp, &created)
	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)
	waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	hm, err := client.Get(cp.URL + "/runs/" + run.RunID + "/heatmap")
	if err != nil {
		t.Fatalf("open heatmap: %v", err)
	}
	defer hm.Body.Close()

	edges := map[string]heatEdgeSnap{}
	done := false
	sc := bufio.NewScanner(hm.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var frame struct {
			Edges []heatEdgeSnap `json:"edges"`
			Done  bool           `json:"done"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &frame); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		for _, e := range frame.Edges {
			edges[e.From+">"+e.To] = e
		}
		if frame.Done {
			done = true
			break
		}
	}

	if !done {
		t.Fatal("heatmap stream never sent a done frame")
	}
	ab, ok := edges["a>b"]
	if !ok || ab.Requests != 3 {
		t.Errorf("a->b edge = %+v (present=%v), want 3 requests", ab, ok)
	}
	if ab.Errors != 0 {
		t.Errorf("a->b errors = %d, want 0 (healthy SUT)", ab.Errors)
	}
	if entry, ok := edges[">a"]; !ok || entry.Requests != 3 {
		t.Errorf("entry ->a = %+v (present=%v), want 3 requests", entry, ok)
	}
}
