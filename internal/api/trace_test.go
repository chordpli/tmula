package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
)

func TestTraceBufSinceAndCap(t *testing.T) {
	b := newTraceBuf()
	for i := 0; i < traceBufCap+50; i++ {
		b.add(load.StepEvent{UserID: "u", To: "a", OK: true})
	}
	// The ring keeps only the most recent traceBufCap events.
	all := b.since(0)
	if len(all) != traceBufCap {
		t.Fatalf("buffered %d events, want cap %d", len(all), traceBufCap)
	}
	// Sequences are monotonic; the oldest retained is past the dropped ones.
	if all[0].Seq <= 50 {
		t.Errorf("oldest retained seq = %d, want > 50 (older dropped)", all[0].Seq)
	}
	// since(highest) returns nothing new.
	if got := b.since(all[len(all)-1].Seq); len(got) != 0 {
		t.Errorf("since(latest) = %d events, want 0", len(got))
	}
}

// TestTraceStreamsStepEvents runs a small traced experiment and reads the
// per-request events from the /trace SSE stream.
func TestTraceStreamsStepEvents(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 3) // graph a -> b, so each user makes 2 requests
	spec.Trace = true

	resp := postJSON(t, cp.URL+"/experiments", spec)
	var created struct{ ID string }
	decode(t, resp, &created)
	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	// Let the run finish; the events stay buffered, so we read them afterward.
	waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	tr, err := client.Get(cp.URL + "/runs/" + run.RunID + "/trace")
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer tr.Body.Close()

	var events []traceWireEvent
	done := false
	sc := bufio.NewScanner(tr.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var frame struct {
			Events []traceWireEvent `json:"events"`
			Done   bool             `json:"done"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &frame); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		events = append(events, frame.Events...)
		if frame.Done {
			done = true
			break
		}
	}

	if !done {
		t.Fatal("trace stream never sent a done frame")
	}
	if len(events) == 0 {
		t.Fatal("no step events streamed")
	}
	// The walk is a -> b: expect entry requests to node a (from empty) and
	// transitions to node b (from a).
	var sawA, sawB bool
	for _, e := range events {
		if e.To == "a" && e.From == "" {
			sawA = true
		}
		if e.To == "b" && e.From == "a" {
			sawB = true
		}
		if !e.OK {
			t.Errorf("healthy SUT event not ok: %+v", e)
		}
	}
	if !sawA || !sawB {
		t.Errorf("expected entry->a and a->b edges in events (sawA=%v sawB=%v)", sawA, sawB)
	}
}

// TestTraceDisabledForUntracedRun: a run that didn't opt in gets a single done
// frame with no events.
func TestTraceDisabledForUntracedRun(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 2) // no Trace
	resp := postJSON(t, cp.URL+"/experiments", spec)
	var created struct{ ID string }
	decode(t, resp, &created)
	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	client := &http.Client{Timeout: 5 * time.Second}
	tr, err := client.Get(cp.URL + "/runs/" + run.RunID + "/trace")
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer tr.Body.Close()
	body := make([]byte, 256)
	n, _ := tr.Body.Read(body)
	frame := string(body[:n])
	if !strings.Contains(frame, `"done":true`) || strings.Contains(frame, `"to"`) {
		t.Errorf("untraced run should get an empty done frame, got %q", frame)
	}
}
