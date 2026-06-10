package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/store"
)

// Defaults applied when Config leaves a field zero.
const (
	defaultBatchSize     = 512
	defaultFlushInterval = 250 * time.Millisecond
	defaultBufferSize    = 4096
)

// ErrClosed is returned by Submit after the pipeline has begun shutting down.
var ErrClosed = errors.New("pipeline: closed")

// metricSink is the subset of store.Store the pipeline needs: a batched metric
// writer. Depending on this narrow interface keeps the pipeline testable with a
// fake and documents that it never reads or touches other entities.
type metricSink interface {
	AppendMetrics(ms []domain.MetricSample) error
}

// compile-time assertion that a *store.MemStore (and thus any Store) is a usable
// sink, so callers can pass their real store directly.
var _ metricSink = (store.Store)(nil)

// Config tunes the pipeline. The zero value is valid and uses the package
// defaults for every field.
type Config struct {
	// BatchSize is the number of buffered samples that triggers a flush. A batch
	// is also flushed when FlushInterval elapses, so partial batches are not held
	// indefinitely.
	BatchSize int
	// FlushInterval bounds how long an accepted sample waits before it is written,
	// even if the batch never fills.
	FlushInterval time.Duration
	// BufferSize is the capacity of the producer-facing channel. It absorbs bursts
	// while the writer is mid-flush; once full, Submit blocks (back-pressure)
	// rather than dropping samples.
	BufferSize int
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = defaultFlushInterval
	}
	if c.BufferSize <= 0 {
		c.BufferSize = defaultBufferSize
	}
	return c
}

// MetricPipeline batches metric samples from many concurrent producers and
// flushes them to a sink. It is safe for concurrent use by multiple producers.
type MetricPipeline struct {
	sink     metricSink
	cfg      Config
	in       chan domain.MetricSample
	done     chan struct{} // closed when the worker has fully drained and exited
	flushErr chan error    // buffered(1): first flush error seen by the worker

	mu     sync.Mutex // guards closed and serialises Close with in-flight Submits
	closed bool
}

// New starts a pipeline that flushes batches to sink. The returned pipeline runs
// a background goroutine until Close is called.
func New(sink metricSink, cfg Config) *MetricPipeline {
	cfg = cfg.withDefaults()
	p := &MetricPipeline{
		sink:     sink,
		cfg:      cfg,
		in:       make(chan domain.MetricSample, cfg.BufferSize),
		done:     make(chan struct{}),
		flushErr: make(chan error, 1),
	}
	go p.run()
	return p
}

// Submit enqueues one sample. It blocks when the buffer is full (back-pressure)
// so no sample is dropped, and returns ErrClosed once Close has been called.
//
// Submit also honours ctx: if ctx is cancelled while the buffer is full it
// returns ctx.Err() instead of blocking forever. Pass context.Background() for
// an unbounded wait.
func (p *MetricPipeline) Submit(ctx context.Context, m domain.MetricSample) error {
	if m.RunID == "" {
		return fmt.Errorf("pipeline: metric runId is required")
	}
	// Hold the lock only across the channel send so Close cannot close `in`
	// between our closed-check and the send (which would panic). The send still
	// blocks under back-pressure, but Close signals shutdown by a separate path
	// and never closes `in` while a Submit holds the lock.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrClosed
	}
	select {
	case p.in <- m:
		p.mu.Unlock()
		return nil
	case <-ctx.Done():
		p.mu.Unlock()
		return ctx.Err()
	}
}

// run is the single consumer goroutine. It owns the batch buffer, so the buffer
// needs no locking, and is the only place AppendMetrics is called.
func (p *MetricPipeline) run() {
	defer close(p.done)

	ticker := time.NewTicker(p.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]domain.MetricSample, 0, p.cfg.BatchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := p.sink.AppendMetrics(batch); err != nil {
			// Record the first error; keep draining so Close still returns and the
			// channel never wedges producers.
			select {
			case p.flushErr <- err:
			default:
			}
		}
		// Reuse the backing array; AppendMetrics must not retain the slice.
		batch = batch[:0]
	}

	for {
		select {
		case m, ok := <-p.in:
			if !ok {
				// Channel closed by Close: flush the final partial batch and exit.
				flush()
				return
			}
			batch = append(batch, m)
			if len(batch) >= p.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Close stops accepting new samples, drains everything already queued, performs a
// final flush, and waits for the background goroutine to exit. It returns the
// first flush error observed over the pipeline's lifetime, if any. Close is
// idempotent; subsequent calls return the same error.
func (p *MetricPipeline) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.done
		return p.firstFlushErr()
	}
	p.closed = true
	// Safe to close now: future Submits see closed==true under the lock and never
	// send, and no Submit can be mid-send because sends happen under this lock.
	close(p.in)
	p.mu.Unlock()

	<-p.done
	return p.firstFlushErr()
}

// firstFlushErr returns the first recorded flush error without blocking.
func (p *MetricPipeline) firstFlushErr() error {
	select {
	case err := <-p.flushErr:
		// Put it back so repeated Close calls report the same error.
		p.flushErr <- err
		return err
	default:
		return nil
	}
}
