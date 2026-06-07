package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// The latency heatmap is the canonical load-test view: a 2-D grid where the
// X axis is wall-clock time since the run started (columns) and the Y axis is a
// latency band (rows), with each cell counting how many requests landed in that
// (time, latency) bucket. Unlike the per-edge flow map it answers "how did the
// latency distribution move over the run?" — surfacing tails and the moment an
// endpoint started slowing down.

// latencyBinWidth is the wall-clock width of one time column. Columns are
// [k*width, (k+1)*width) measured from the run's start. 500ms keeps a typical
// run to a few dozen readable columns while still showing within-run movement.
const latencyBinWidth = 500 * time.Millisecond

// latencyBounds are the inclusive-low/exclusive-high upper edges (ms) of the
// latency bands, low to high. A request with latency < bounds[i] (and >=
// bounds[i-1]) lands in row i; anything >= the last bound lands in the final,
// unbounded band, so there are len(latencyBounds)+1 rows.
var latencyBounds = []int{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// latencyRows is the number of latency bands (one per bound plus the unbounded
// top band).
func latencyRows() int { return len(latencyBounds) + 1 }

// latencyRowFor maps a latency in ms onto its band index.
func latencyRowFor(ms float64) int {
	for i, b := range latencyBounds {
		if ms < float64(b) {
			return i
		}
	}
	return len(latencyBounds) // unbounded top band
}

// latencyHeat aggregates request latencies into a time x latency grid. Writes
// come from many session goroutines, so a mutex guards the sparse column map;
// the work per request is a map lookup plus one increment. Columns are created
// lazily, so memory is bounded by the run's wall-clock length / latencyBinWidth.
type latencyHeat struct {
	start  time.Time
	mu     sync.Mutex
	grid   map[int][]int64 // time column -> per-row counts (len == latencyRows())
	maxCol int
}

// newLatencyHeat starts a grid whose time columns are measured from start.
func newLatencyHeat(start time.Time) *latencyHeat {
	return &latencyHeat{start: start, grid: make(map[int][]int64)}
}

// record folds one observed latency (ms) seen at time `at` into its cell.
func (h *latencyHeat) record(latencyMs float64, at time.Time) {
	col := int(at.Sub(h.start) / latencyBinWidth)
	if col < 0 {
		col = 0 // a clock skew before start clamps into the first column
	}
	row := latencyRowFor(latencyMs)
	h.mu.Lock()
	r := h.grid[col]
	if r == nil {
		r = make([]int64, latencyRows())
		h.grid[col] = r
	}
	r[row]++
	if col > h.maxCol {
		h.maxCol = col
	}
	h.mu.Unlock()
}

// latencyRowSnap labels one latency band for the client's Y axis. HiMs == 0
// marks the unbounded top band (e.g. "5000+").
type latencyRowSnap struct {
	LoMs  int    `json:"loMs"`
	HiMs  int    `json:"hiMs"`
	Label string `json:"label"`
}

// latencyRowsMeta returns the fixed band labels, low to high.
func latencyRowsMeta() []latencyRowSnap {
	out := make([]latencyRowSnap, 0, latencyRows())
	lo := 0
	for _, b := range latencyBounds {
		out = append(out, latencyRowSnap{LoMs: lo, HiMs: b, Label: fmt.Sprintf("%d–%d", lo, b)})
		lo = b
	}
	out = append(out, latencyRowSnap{LoMs: lo, HiMs: 0, Label: fmt.Sprintf("%d+", lo)})
	return out
}

// snapshot returns a dense row-major grid (cells[row][col]) plus the busiest
// cell's count for color scaling. cols spans 0..maxCol; an empty grid yields
// zero-width rows and a zero max.
func (h *latencyHeat) snapshot() (cells [][]int64, maxCount int64) {
	rows := latencyRows()
	h.mu.Lock()
	defer h.mu.Unlock()
	cols := 0
	if len(h.grid) > 0 {
		cols = h.maxCol + 1
	}
	out := make([][]int64, rows)
	for r := range out {
		out[r] = make([]int64, cols)
	}
	for col, rc := range h.grid {
		for r := 0; r < rows; r++ {
			c := rc[r]
			out[r][col] = c
			if c > maxCount {
				maxCount = c
			}
		}
	}
	return out, maxCount
}

// streamLatencyHeatmap streams the time x latency grid for a run as SSE, powering
// the latency heatmap. Frames are
// {"binWidthMs":n,"rows":[{loMs,hiMs,label},...],"cells":[[...]...],"maxCount":n,"done":bool};
// rows are fixed latency bands (low to high) and cells[row][col] is the request
// count in that band during time column col (col*binWidthMs since start). A run
// without visualization enabled gets a single done frame.
func (s *Server) streamLatencyHeatmap(w http.ResponseWriter, r *http.Request) {
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

	binMs := int(latencyBinWidth / time.Millisecond)
	rowsMeta := latencyRowsMeta()
	send := func(cells [][]int64, maxCount int64, done bool) {
		if cells == nil {
			cells = [][]int64{}
		}
		b, _ := json.Marshal(map[string]any{
			"binWidthMs": binMs,
			"rows":       rowsMeta,
			"cells":      cells,
			"maxCount":   maxCount,
			"done":       done,
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	if rs.latency == nil {
		send(nil, 0, true)
		return
	}

	// Unlike the per-edge flow map (a fixed number of edges), this grid gains a
	// column every latencyBinWidth of wall clock, so a full snapshot grows O(time).
	// Latency distributions move slowly, so refresh once a second rather than 4x —
	// that quarters the bytes re-sent on a long run while still feeling live.
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		status, _ := rs.snapshotStatus()
		done := status != domain.RunRunning && status != domain.RunPending
		cells, maxCount := rs.latency.snapshot()
		send(cells, maxCount, done)
		if done {
			return
		}
	}
}
