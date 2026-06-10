// Package pipeline fans high-frequency metric samples in from many concurrent
// producers and flushes them to a store.Store in batches.
//
// A load run drives many virtual users across many workers, each emitting
// metric samples far faster than a row-at-a-time insert can absorb. MetricPipeline
// decouples producers from the store: producers hand samples to a buffered
// channel (a non-blocking, in-process queue) and a single background goroutine
// accumulates them and flushes via Store.AppendMetrics either when a batch fills
// or when a flush interval elapses. Close drains everything still queued and
// performs a final flush, so no accepted sample is lost.
package pipeline
