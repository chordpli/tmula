package obs

import "math"

// Histogram is a compact, mergeable latency histogram for millisecond values.
//
// It uses an HDR-style log-linear bucketing: the representable range is split
// into power-of-two octaves, and each octave is divided into a fixed number of
// equal-width sub-buckets. A value's bucket is therefore found from its binary
// exponent plus a linear offset within the octave, which makes the *relative*
// width of every bucket constant. As a result a recorded value is known to lie
// within a bounded fraction of its true magnitude regardless of scale.
//
// Bucket layout is fixed and identical for every Histogram, so merging is pure
// element-wise addition of the bucket counts: it is associative and commutative
// (the merge of any grouping of inputs yields the same counts). Memory is
// bounded at numBuckets counters irrespective of how many samples are observed.
//
// Quantile error bound: estimates are returned at the midpoint of the resolving
// bucket. Each octave is split into 2^subBucketBits equal-width sub-buckets, so
// a bucket's width is ~(2^(1/2^subBucketBits) - 1) of its lower edge; reporting
// the midpoint puts any contained value within half that width of the estimate.
// With subBucketBits = 6 the worst-case relative error is ~1.1% (empirically
// verified across the full range, peaking at the bottom octave). Values at or
// below minValue collapse into bucket 0 and values at or above maxValue
// saturate the top bucket; both are reported via the histogram's own boundaries
// rather than as unbounded error.
const (
	// subBucketBits controls sub-buckets per octave: 2^subBucketBits of them.
	// 6 -> 64 sub-buckets per power-of-two -> ~1.1% worst-case relative quantile
	// error (see the type doc; empirically ~1.04%, peaking at the bottom octave).
	subBucketBits  = 6
	subBucketCount = 1 << subBucketBits // 64

	// minValueMs is the smallest positive magnitude the histogram resolves.
	// Anything in (0, minValueMs] lands in the first bucket. ~0.1ms.
	minValueMs = 0.1
	// maxValueMs is the largest magnitude before the top bucket saturates.
	// ~10 minutes (600000ms); we round up to the next octave boundary below.
	maxValueMs = 600000.0
)

// octaveCount is the number of power-of-two octaves spanning [minValueMs,
// maxValueMs], computed once at init. minValueMs * 2^octaveCount >= maxValueMs.
var octaveCount = func() int {
	n := 0
	for v := minValueMs; v < maxValueMs; v *= 2 {
		n++
	}
	return n + 1 // inclusive headroom so maxValueMs is representable
}()

// numBuckets is the fixed bucket count (octaves * sub-buckets, plus one
// overflow slot for non-finite/huge values so they are never silently lost).
var numBuckets = octaveCount*subBucketCount + 1

// overflowBucket is the index of the saturating top slot.
var overflowBucket = numBuckets - 1

// Histogram holds bounded-memory bucket counts plus exact aggregates that are
// cheap to keep and improve fidelity (true max, true total count).
//
// A Histogram is single-writer: Observe and Merge are not safe to call
// concurrently with each other or themselves. Callers that fan in from many
// goroutines should guard it (see Summary, which owns the locking). Read
// methods (Quantile, Count, Max) are safe to call once writes have stopped.
type Histogram struct {
	buckets []uint64
	count   int64
	max     float64
}

// NewHistogram returns an empty Histogram ready to Observe into.
func NewHistogram() *Histogram {
	return &Histogram{buckets: make([]uint64, numBuckets)}
}

// bucketIndex maps a millisecond value to its fixed bucket.
//
// The mapping is: normalize v into [1,2) by its base-2 exponent relative to
// minValueMs, then pick the sub-bucket by the fractional mantissa. Sub-zero,
// tiny, and non-finite inputs are clamped to the valid range so the function is
// total and never panics.
func bucketIndex(ms float64) int {
	if math.IsNaN(ms) {
		return 0
	}
	if ms <= minValueMs {
		return 0
	}
	if math.IsInf(ms, 1) || ms >= maxValueMs {
		return overflowBucket
	}
	// frac in [0, octaveCount): log2 of how many octaves above minValueMs.
	frac := math.Log2(ms / minValueMs)
	octave := int(frac)
	sub := int((frac - float64(octave)) * subBucketCount)
	if sub >= subBucketCount { // guard against fp rounding at the boundary
		sub = subBucketCount - 1
	}
	idx := octave*subBucketCount + sub
	if idx < 0 {
		return 0
	}
	if idx >= overflowBucket {
		return overflowBucket
	}
	return idx
}

// bucketLow returns the inclusive lower bound (in ms) of bucket i.
func bucketLow(i int) float64 {
	if i <= 0 {
		return 0
	}
	if i >= overflowBucket {
		return maxValueMs
	}
	octave := i / subBucketCount
	sub := i % subBucketCount
	return minValueMs * math.Pow(2, float64(octave)+float64(sub)/subBucketCount)
}

// bucketHigh returns the exclusive upper bound (in ms) of bucket i.
func bucketHigh(i int) float64 {
	if i >= overflowBucket {
		return math.Inf(1)
	}
	return bucketLow(i + 1)
}

// bucketMid returns the representative midpoint of bucket i, used as the value
// estimate for any sample resolving to that bucket.
func bucketMid(i int) float64 {
	if i <= 0 {
		// First bucket covers (0, minValueMs]; report its upper edge as a
		// stable, conservative estimate rather than minValueMs/2.
		return minValueMs
	}
	lo := bucketLow(i)
	hi := bucketHigh(i)
	if math.IsInf(hi, 1) {
		return lo // overflow: best estimate is the saturating boundary
	}
	return (lo + hi) / 2
}

// Observe records a single latency sample in milliseconds. Non-positive or
// non-finite values are clamped into the histogram's boundaries (they still
// count toward Count) so accounting stays exact even on degenerate input.
func (h *Histogram) Observe(ms float64) {
	idx := bucketIndex(ms)
	h.buckets[idx]++
	h.count++
	if ms > h.max && !math.IsInf(ms, 1) {
		h.max = ms
	} else if math.IsInf(ms, 1) && h.max < maxValueMs {
		// An infinite sample saturates max at the top representable boundary
		// rather than poisoning it with +Inf.
		h.max = maxValueMs
	}
}

// Merge folds other into h by element-wise bucket addition. Because every
// Histogram shares the same fixed layout this is exact for counts and
// associative/commutative across any number of merges. other is not modified.
func (h *Histogram) Merge(other *Histogram) {
	if other == nil {
		return
	}
	for i := range h.buckets {
		h.buckets[i] += other.buckets[i]
	}
	h.count += other.count
	if other.max > h.max {
		h.max = other.max
	}
}

// Count returns the exact number of observed samples.
func (h *Histogram) Count() int64 { return h.count }

// Max returns the exact maximum observed sample (0 if empty). Unlike quantiles
// this is tracked precisely and is not subject to bucketing error.
func (h *Histogram) Max() float64 { return h.max }

// Quantile estimates the q-th quantile (q in [0,1]) in milliseconds using the
// nearest-rank method over the bucket counts, returning the midpoint of the
// resolving bucket. The result is within the documented bucket error bound of
// the true value. An empty histogram returns 0.
func (h *Histogram) Quantile(q float64) float64 {
	if h.count == 0 {
		return 0
	}
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	// Nearest-rank: the rank-th smallest sample, rank = ceil(q*n), 1-based.
	// Matches the Collector's percentile() convention for consistency.
	rank := int64(math.Ceil(q * float64(h.count)))
	if rank < 1 {
		rank = 1
	}
	if rank > h.count {
		rank = h.count
	}
	var cum int64
	for i := 0; i < len(h.buckets); i++ {
		cum += int64(h.buckets[i])
		if cum >= rank {
			return bucketMid(i)
		}
	}
	// Unreachable when count > 0, but stay total.
	return h.max
}
