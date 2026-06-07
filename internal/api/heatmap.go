package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
)

// heatEdge accumulates the request and error counts on one (from -> to)
// transition. It is updated with atomics so the hot path takes no lock.
type heatEdge struct {
	from, to string
	req      atomic.Int64
	err      atomic.Int64
}

// heatAgg aggregates traffic per graph edge for the large-scale heatmap view.
// Unlike the per-event trace buffer it is fixed-size — one counter pair per edge
// — so it scales to millions of requests at O(1) per request. The edge set is
// precomputed from the graph (read-only after construction), so record needs no
// lock: it is a map read plus two atomic adds.
type heatAgg struct {
	idx   map[string]int // "from>to" -> edges slot; read-only after newHeatAgg
	edges []*heatEdge
}

// newHeatAgg precomputes a counter slot for each graph edge and each entry
// ("" -> node) transition.
func newHeatAgg(g domain.ScenarioGraph) *heatAgg {
	h := &heatAgg{idx: make(map[string]int)}
	add := func(from, to string) {
		k := from + ">" + to
		if _, ok := h.idx[k]; ok {
			return
		}
		h.idx[k] = len(h.edges)
		h.edges = append(h.edges, &heatEdge{from: from, to: to})
	}
	for _, n := range g.Nodes {
		add("", string(n.ID)) // entry into each node
	}
	for _, e := range g.Edges {
		add(string(e.From), string(e.To))
	}
	return h
}

// record folds one step event into its edge counters. It is safe for concurrent
// use and cheap. An event whose transition is not a known edge is ignored
// (graph-following walks never produce one).
func (h *heatAgg) record(e load.StepEvent) {
	if i, ok := h.idx[e.From+">"+e.To]; ok {
		h.edges[i].req.Add(1)
		if !e.OK {
			h.edges[i].err.Add(1)
		}
	}
}

// heatEdgeSnap is one edge's tallies as streamed to the browser.
type heatEdgeSnap struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Requests int64  `json:"requests"`
	Errors   int64  `json:"errors"`
}

// snapshot returns the current tallies for every edge that has seen traffic.
func (h *heatAgg) snapshot() []heatEdgeSnap {
	out := make([]heatEdgeSnap, 0, len(h.edges))
	for _, e := range h.edges {
		r := e.req.Load()
		if r == 0 {
			continue
		}
		out = append(out, heatEdgeSnap{From: e.from, To: e.to, Requests: r, Errors: e.err.Load()})
	}
	return out
}

// streamHeatmap streams periodic per-edge traffic aggregates for a run as SSE,
// powering the large-scale heatmap (edge glow by request rate, color by error
// rate). Frames are {"edges":[{from,to,requests,errors},...], "done":bool}; the
// final frame has done=true. A run without the heatmap enabled gets one done
// frame.
func (s *Server) streamHeatmap(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	s.mu.Lock()
	rs := s.runs[id]
	s.mu.Unlock()
	if rs == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", id))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("api: streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	send := func(edges []heatEdgeSnap, done bool) {
		if edges == nil {
			edges = []heatEdgeSnap{}
		}
		b, _ := json.Marshal(map[string]any{"edges": edges, "done": done})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	if rs.heat == nil {
		send(nil, true)
		return
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		status, _ := rs.snapshotStatus()
		done := status != domain.RunRunning && status != domain.RunPending
		send(rs.heat.snapshot(), done)
		if done {
			return
		}
	}
}
