package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// fakeSink is a concurrency-safe metricSink that records every sample it is
// asked to write. It copies each batch because the pipeline reuses the backing
// array of the slice it passes to AppendMetrics (so the sink must not retain it).
type fakeSink struct {
	mu       sync.Mutex
	samples  []domain.MetricSample
	batches  int
	maxBatch int

	failNext atomic.Bool // when set, the next AppendMetrics returns an error
}

func (f *fakeSink) AppendMetrics(ms []domain.MetricSample) error {
	if f.failNext.Swap(false) {
		return errors.New("fake: induced flush failure")
	}
	cp := make([]domain.MetricSample, len(ms))
	copy(cp, ms)
	f.mu.Lock()
	f.samples = append(f.samples, cp...)
	f.batches++
	if len(cp) > f.maxBatch {
		f.maxBatch = len(cp)
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeSink) snapshot() ([]domain.MetricSample, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.MetricSample, len(f.samples))
	copy(out, f.samples)
	return out, f.batches, f.maxBatch
}

// TestPipelineConcurrentNoLoss is the core guarantee: many producers submit
// concurrently, every accepted sample reaches the sink exactly once, batches are
// actually formed, and Close drains the tail. Run with -race.
func TestPipelineConcurrentNoLoss(t *testing.T) {
	const producers = 32
	const perProducer = 1000
	const want = producers * perProducer

	sink := &fakeSink{}
	p := New(sink, Config{
		BatchSize:     128,
		FlushInterval: 5 * time.Millisecond,
		BufferSize:    256, // deliberately small to force back-pressure + batching
	})

	var wg sync.WaitGroup
	for g := 0; g < producers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				m := domain.MetricSample{
					RunID:      domain.ID(fmt.Sprintf("run-%d", g)),
					StatusCode: i,
					WorkerID:   fmt.Sprintf("w-%d", g),
				}
				if err := p.Submit(context.Background(), m); err != nil {
					t.Errorf("Submit: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, batches, maxBatch := sink.snapshot()
	if len(got) != want {
		t.Fatalf("sample count = %d, want %d (loss or duplication)", len(got), want)
	}
	// Every sample must be the exact one a producer sent — verify the per-producer
	// counts and the set of status codes, which detects loss, duplication, or
	// corruption that a bare count could miss.
	counts := make(map[domain.ID]int)
	seen := make(map[string]bool)
	for _, m := range got {
		counts[m.RunID]++
		key := fmt.Sprintf("%s/%d", m.RunID, m.StatusCode)
		if seen[key] {
			t.Fatalf("duplicate sample %s", key)
		}
		seen[key] = true
	}
	for g := 0; g < producers; g++ {
		rid := domain.ID(fmt.Sprintf("run-%d", g))
		if counts[rid] != perProducer {
			t.Fatalf("run %s got %d samples, want %d", rid, counts[rid], perProducer)
		}
	}
	if batches == 0 {
		t.Fatal("expected at least one batch flush")
	}
	if maxBatch < 2 {
		t.Errorf("expected batching to coalesce samples, max batch was %d", maxBatch)
	}
	t.Logf("drained %d samples across %d batches (max batch %d)", len(got), batches, maxBatch)
}

// TestPipelineCloseDrainsTail submits fewer than one batch worth of samples and
// closes immediately. Without a drain-on-close the tail would be lost.
func TestPipelineCloseDrainsTail(t *testing.T) {
	sink := &fakeSink{}
	// Huge batch + long interval: nothing flushes until Close forces it.
	p := New(sink, Config{BatchSize: 10_000, FlushInterval: time.Hour, BufferSize: 64})

	const n = 7
	for i := 0; i < n; i++ {
		if err := p.Submit(context.Background(), domain.MetricSample{RunID: "r", StatusCode: i}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, _, _ := sink.snapshot(); len(got) != n {
		t.Fatalf("Close did not drain tail: got %d, want %d", len(got), n)
	}
}

// TestPipelineSubmitAfterClose proves Submit rejects cleanly (no panic on a
// closed channel) once the pipeline is closing, and that Close is idempotent.
func TestPipelineSubmitAfterClose(t *testing.T) {
	sink := &fakeSink{}
	p := New(sink, Config{})

	_ = p.Submit(context.Background(), domain.MetricSample{RunID: "r", StatusCode: 1})
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.Submit(context.Background(), domain.MetricSample{RunID: "r", StatusCode: 2}); !errors.Is(err, ErrClosed) {
		t.Errorf("Submit after Close = %v, want ErrClosed", err)
	}
	// Second Close is a no-op and returns the same (nil) error.
	if err := p.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if got, _, _ := sink.snapshot(); len(got) != 1 {
		t.Errorf("only the pre-close sample should land, got %d", len(got))
	}
}

// TestPipelineSubmitValidatesRunID rejects an empty runId without enqueuing it.
func TestPipelineSubmitValidatesRunID(t *testing.T) {
	p := New(&fakeSink{}, Config{})
	defer func() { _ = p.Close() }()
	if err := p.Submit(context.Background(), domain.MetricSample{StatusCode: 1}); err == nil {
		t.Error("Submit with empty runId should error")
	}
}

// TestPipelineSubmitContextCancel proves a producer is not wedged forever when
// the buffer is full: a cancelled context unblocks Submit with ctx.Err().
func TestPipelineSubmitContextCancel(t *testing.T) {
	// Block the worker so the buffer fills and stays full.
	release := make(chan struct{})
	blocking := &blockingSink{release: release}
	p := New(blocking, Config{BatchSize: 1, FlushInterval: time.Hour, BufferSize: 1})
	t.Cleanup(func() {
		close(release)
		_ = p.Close()
	})

	// Fill the buffer and hand one sample to the worker (which blocks in the sink).
	ctx := context.Background()
	_ = p.Submit(ctx, domain.MetricSample{RunID: "r", StatusCode: 1})
	_ = p.Submit(ctx, domain.MetricSample{RunID: "r", StatusCode: 2})

	cctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- p.Submit(cctx, domain.MetricSample{RunID: "r", StatusCode: 3})
	}()
	// Give the goroutine a moment to block on the full buffer, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Submit under cancel = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Submit did not return after context cancel (back-pressure deadlock)")
	}
}

// TestPipelineFlushErrorSurfaced proves a flush failure is reported by Close and
// does not wedge producers (the worker keeps draining after an error).
func TestPipelineFlushErrorSurfaced(t *testing.T) {
	sink := &fakeSink{}
	sink.failNext.Store(true)
	p := New(sink, Config{BatchSize: 2, FlushInterval: time.Hour, BufferSize: 16})

	for i := 0; i < 2; i++ { // fills a batch -> triggers the failing flush
		if err := p.Submit(context.Background(), domain.MetricSample{RunID: "r", StatusCode: i}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	// A follow-up sample must still be accepted (producers not wedged).
	if err := p.Submit(context.Background(), domain.MetricSample{RunID: "r", StatusCode: 99}); err != nil {
		t.Fatalf("Submit after flush error: %v", err)
	}
	if err := p.Close(); err == nil {
		t.Fatal("Close should surface the flush error")
	}
}

// blockingSink blocks in AppendMetrics until release is closed. Used to hold the
// worker so the input buffer fills for the back-pressure test.
type blockingSink struct {
	release chan struct{}
	once    sync.Once
}

func (b *blockingSink) AppendMetrics(ms []domain.MetricSample) error {
	b.once.Do(func() { <-b.release })
	return nil
}
