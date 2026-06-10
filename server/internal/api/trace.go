package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

const (
	// traceMaxUsers caps the run size for which live per-request tracing is
	// honored. It is an inspect/demo view (watch each user), not a
	// millions-scale feature, so larger runs silently skip it.
	traceMaxUsers = 200
	// traceBufCap bounds the per-run event ring; older events are dropped when a
	// consumer can't keep up (a live view tolerates sampling).
	traceBufCap = 1024
)

// traceSmallEnough reports whether a run is small enough to trace per request.
func traceSmallEnough(spec RunSpec) bool {
	if spec.IsOpen() {
		c := spec.Workload.MaxConcurrency
		return c > 0 && c <= traceMaxUsers
	}
	// Size off the effective pool (PoolSize), so a small closed run requested as a
	// count — with no shipped Users array — still opts into per-request tracing.
	n := spec.PoolSize()
	return n > 0 && n <= traceMaxUsers
}

// traceWireEvent is one step event as streamed to the browser.
type traceWireEvent struct {
	Seq       uint64  `json:"seq"`
	UserID    string  `json:"userId"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	Status    int     `json:"status"`
	LatencyMs float64 `json:"latencyMs"`
	OK        bool    `json:"ok"`
}

// traceBuf is a bounded, concurrency-safe ring of recent step events with a
// monotonic sequence, so the SSE handler streams only what is new since the
// client's last position.
type traceBuf struct {
	mu   sync.Mutex
	seq  uint64
	ring []traceWireEvent
}

func newTraceBuf() *traceBuf { return &traceBuf{ring: make([]traceWireEvent, 0, traceBufCap)} }

// add records one event. It is safe for concurrent use and cheap, since it runs
// on the request hot path of every session goroutine.
func (b *traceBuf) add(e load.StepEvent) {
	b.mu.Lock()
	b.seq++
	we := traceWireEvent{
		Seq: b.seq, UserID: e.UserID, From: e.From, To: e.To,
		Status: e.Status, LatencyMs: e.LatencyMs, OK: e.OK,
	}
	if len(b.ring) < traceBufCap {
		b.ring = append(b.ring, we)
	} else {
		copy(b.ring, b.ring[1:]) // drop the oldest
		b.ring[len(b.ring)-1] = we
	}
	b.mu.Unlock()
}

// since returns the buffered events with Seq greater than seq, in order.
func (b *traceBuf) since(seq uint64) []traceWireEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []traceWireEvent
	for _, e := range b.ring {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	return out
}

// streamTrace streams per-request step events for a run as Server-Sent Events,
// powering the live traffic graph. Each frame is
// {"events":[{seq,userId,from,to,status,latencyMs,ok},...], "done":bool}; the
// final frame has done=true and then the stream closes. A run without tracing
// enabled gets a single done frame.
func (s *Server) streamTrace(w http.ResponseWriter, r *http.Request) {
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

	send := func(events []traceWireEvent, done bool) {
		if events == nil {
			events = []traceWireEvent{}
		}
		b, _ := json.Marshal(map[string]any{"events": events, "done": done})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	if rs.trace == nil {
		send(nil, true) // tracing was not enabled for this run
		return
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastSeq uint64
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		evs := rs.trace.since(lastSeq)
		if n := len(evs); n > 0 {
			lastSeq = evs[n-1].Seq
		}
		status, _ := rs.snapshotStatus()
		done := status != domain.RunRunning && status != domain.RunPending
		send(evs, done)
		if done {
			return
		}
	}
}
