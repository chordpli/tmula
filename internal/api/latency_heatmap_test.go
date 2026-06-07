package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

func TestLatencyRowFor(t *testing.T) {
	cases := []struct {
		ms   float64
		want int
	}{
		{0, 0}, {3, 0}, {5, 1}, {7, 1}, {10, 2}, {30, 3}, {99, 4},
		{250, 6}, {999, 7}, {5000, 10}, {99999, 10},
	}
	for _, c := range cases {
		if got := latencyRowFor(c.ms); got != c.want {
			t.Errorf("latencyRowFor(%v) = %d, want %d", c.ms, got, c.want)
		}
	}
}

func TestLatencyHeatRecordSnapshot(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	h := newLatencyHeat(t0)
	h.record(3, t0)                            // row 0, col 0
	h.record(7, t0.Add(100*time.Millisecond))  // row 1, col 0
	h.record(30, t0.Add(600*time.Millisecond)) // row 3, col 1 (600/500 = 1)
	h.record(30, t0.Add(700*time.Millisecond)) // row 3, col 1 again
	h.record(-50, t0.Add(-9*time.Millisecond)) // clock skew clamps to col 0, row 0

	cells, max, _ := h.snapshot()
	if len(cells) != latencyRows() {
		t.Fatalf("rows = %d, want %d", len(cells), latencyRows())
	}
	if len(cells[0]) != 2 {
		t.Fatalf("cols = %d, want 2", len(cells[0]))
	}
	if cells[0][0] != 2 { // 3ms + the clamped skew
		t.Errorf("cells[0][0] = %d, want 2", cells[0][0])
	}
	if cells[1][0] != 1 {
		t.Errorf("cells[1][0] = %d, want 1", cells[1][0])
	}
	if cells[3][1] != 2 {
		t.Errorf("cells[3][1] = %d, want 2", cells[3][1])
	}
	if max != 2 {
		t.Errorf("maxCount = %d, want 2", max)
	}
}

func TestLatencyHeatSnapshotEmpty(t *testing.T) {
	h := newLatencyHeat(time.Unix(0, 0))
	cells, max, _ := h.snapshot()
	if len(cells) != latencyRows() {
		t.Fatalf("rows = %d, want %d", len(cells), latencyRows())
	}
	for r, row := range cells {
		if len(row) != 0 {
			t.Errorf("row %d width = %d, want 0 for an empty grid", r, len(row))
		}
	}
	if max != 0 {
		t.Errorf("maxCount = %d, want 0", max)
	}
}

// TestLatencyHeatSnapshotDownsamplesLongRun: a run long enough to exceed
// latencyMaxCols columns must stream a downsampled grid (<= the cap) with a
// proportionally widened bin, preserving total counts — so a soak's frame size
// stays bounded instead of growing with run length.
func TestLatencyHeatSnapshotDownsamplesLongRun(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	h := newLatencyHeat(t0)
	const span = 1000 // 1000 columns at the base bin = well past latencyMaxCols
	for c := 0; c < span; c++ {
		h.record(7, t0.Add(time.Duration(c)*latencyBinWidth)) // one sample per column (row 1)
	}

	cells, max, binMs := h.snapshot()
	if len(cells) != latencyRows() {
		t.Fatalf("rows = %d, want %d", len(cells), latencyRows())
	}
	if cols := len(cells[0]); cols > latencyMaxCols {
		t.Errorf("cols = %d, want <= %d (downsampled)", cols, latencyMaxCols)
	}
	base := int(latencyBinWidth / time.Millisecond)
	if binMs <= base {
		t.Errorf("binWidthMs = %d, want > %d (widened by the merge)", binMs, base)
	}
	var total int64
	for _, row := range cells {
		for _, v := range row {
			total += v
		}
	}
	if total != span {
		t.Errorf("total counts = %d, want %d (merge must preserve counts)", total, span)
	}
	if max < 1 {
		t.Errorf("maxCount = %d, want >= 1", max)
	}
}

// TestLatencyHeatmapStreamsCells runs a traced experiment and reads the time x
// latency grid from the /latency-heatmap SSE stream.
func TestLatencyHeatmapStreamsCells(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 3) // graph a -> b, 3 users
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
	hm, err := client.Get(cp.URL + "/runs/" + run.RunID + "/latency-heatmap")
	if err != nil {
		t.Fatalf("open latency-heatmap: %v", err)
	}
	defer hm.Body.Close()

	type frame struct {
		BinWidthMs int              `json:"binWidthMs"`
		Rows       []latencyRowSnap `json:"rows"`
		Cells      [][]int64        `json:"cells"`
		MaxCount   int64            `json:"maxCount"`
		Done       bool             `json:"done"`
	}
	var last frame
	done := false
	sc := bufio.NewScanner(hm.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var f frame
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &f); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		last = f
		if f.Done {
			done = true
			break
		}
	}

	if !done {
		t.Fatal("latency-heatmap stream never sent a done frame")
	}
	if last.BinWidthMs != int(latencyBinWidth/time.Millisecond) {
		t.Errorf("binWidthMs = %d, want %d", last.BinWidthMs, int(latencyBinWidth/time.Millisecond))
	}
	if len(last.Rows) != latencyRows() {
		t.Errorf("rows = %d, want %d", len(last.Rows), latencyRows())
	}
	var total int64
	for _, row := range last.Cells {
		for _, c := range row {
			total += c
		}
	}
	if total < 1 {
		t.Errorf("total recorded latencies = %d, want >= 1 (3 users walked a->b)", total)
	}
	if last.MaxCount < 1 {
		t.Errorf("maxCount = %d, want >= 1", last.MaxCount)
	}
}
