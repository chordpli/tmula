package obs

import (
	"math"
	"sort"
	"sync"
	"testing"
)

// histTolerance is the relative error we allow when asserting quantile
// estimates against the true value. The documented worst-case bucket error is
// ~1.1%; we assert against 1.5% to leave a small safety margin for rank
// rounding at distribution edges.
const histTolerance = 0.015

// trueQuantile returns the nearest-rank quantile of data (need not be sorted),
// matching Histogram.Quantile's ranking convention.
func trueQuantile(data []float64, q float64) float64 {
	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)
	rank := int(math.Ceil(q * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func assertClose(t *testing.T, label string, got, want, tol float64) {
	t.Helper()
	if want == 0 {
		if math.Abs(got) > tol {
			t.Errorf("%s = %v, want ~0 (tol %v)", label, got, tol)
		}
		return
	}
	rel := math.Abs(got-want) / math.Abs(want)
	if rel > tol {
		t.Errorf("%s = %v, want %v (rel err %.4f%% > tol %.4f%%)", label, got, want, rel*100, tol*100)
	}
}

func TestHistogramQuantileAccuracyUniform(t *testing.T) {
	h := NewHistogram()
	data := make([]float64, 0, 10000)
	for i := 1; i <= 10000; i++ {
		v := float64(i)
		h.Observe(v)
		data = append(data, v)
	}
	if h.Count() != 10000 {
		t.Fatalf("count = %d, want 10000", h.Count())
	}
	for _, q := range []float64{0.5, 0.95, 0.99} {
		assertClose(t, "q"+formatQ(q), h.Quantile(q), trueQuantile(data, q), histTolerance)
	}
	// Max is tracked exactly, not bucketed.
	if h.Max() != 10000 {
		t.Errorf("max = %v, want 10000", h.Max())
	}
}

// TestHistogramQuantileAccuracyWideRange stresses the log-bucketing across many
// octaves (0.1ms .. ~100s), where a linear histogram would either explode or
// lose all low-end resolution.
func TestHistogramQuantileAccuracyWideRange(t *testing.T) {
	h := NewHistogram()
	var data []float64
	// Geometric spread so the values exercise the whole representable range.
	for v := 0.1; v < 100000; v *= 1.05 {
		h.Observe(v)
		data = append(data, v)
	}
	for _, q := range []float64{0.1, 0.5, 0.9, 0.95, 0.99} {
		assertClose(t, "q"+formatQ(q), h.Quantile(q), trueQuantile(data, q), histTolerance)
	}
}

func TestHistogramMergeEquivalence(t *testing.T) {
	// histA over one dataset, histB over another. Merging them must match a
	// single histogram fed both datasets, within bucket tolerance.
	a := NewHistogram()
	b := NewHistogram()
	combined := NewHistogram()

	var all []float64
	for i := 1; i <= 5000; i++ { // low range -> A
		v := float64(i)
		a.Observe(v)
		combined.Observe(v)
		all = append(all, v)
	}
	for i := 0; i < 5000; i++ { // high range -> B
		v := 2000 + float64(i)*3
		b.Observe(v)
		combined.Observe(v)
		all = append(all, v)
	}

	a.Merge(b)

	if a.Count() != combined.Count() {
		t.Fatalf("merged count = %d, want %d", a.Count(), combined.Count())
	}
	if a.Max() != combined.Max() {
		t.Errorf("merged max = %v, want %v", a.Max(), combined.Max())
	}
	for _, q := range []float64{0.25, 0.5, 0.75, 0.95, 0.99} {
		// Merged histogram vs single-fed histogram must agree exactly (identical
		// bucket counts), and both must be within tolerance of the truth.
		if a.Quantile(q) != combined.Quantile(q) {
			t.Errorf("q%s merged=%v combined=%v: bucket counts diverged", formatQ(q), a.Quantile(q), combined.Quantile(q))
		}
		assertClose(t, "q"+formatQ(q), a.Quantile(q), trueQuantile(all, q), histTolerance)
	}
}

func TestHistogramMergeAssociative(t *testing.T) {
	// (A merge B) merge C  ==  A merge (B merge C), exact bucket equality.
	mk := func(base float64) *Histogram {
		h := NewHistogram()
		for i := 0; i < 1000; i++ {
			h.Observe(base + float64(i))
		}
		return h
	}
	left := func() *Histogram {
		a, b, c := mk(1), mk(500), mk(9000)
		a.Merge(b)
		a.Merge(c)
		return a
	}()
	right := func() *Histogram {
		a, b, c := mk(1), mk(500), mk(9000)
		b.Merge(c)
		a.Merge(b)
		return a
	}()
	if left.Count() != right.Count() {
		t.Fatalf("counts differ: %d vs %d", left.Count(), right.Count())
	}
	for i := range left.buckets {
		if left.buckets[i] != right.buckets[i] {
			t.Fatalf("bucket %d differs: %d vs %d (not associative)", i, left.buckets[i], right.buckets[i])
		}
	}
}

func TestHistogramMergeNil(t *testing.T) {
	h := NewHistogram()
	h.Observe(42)
	h.Merge(nil) // must not panic
	if h.Count() != 1 {
		t.Fatalf("count = %d after nil merge, want 1", h.Count())
	}
}

func TestHistogramEmpty(t *testing.T) {
	h := NewHistogram()
	if h.Count() != 0 {
		t.Errorf("count = %d, want 0", h.Count())
	}
	if h.Max() != 0 {
		t.Errorf("max = %v, want 0", h.Max())
	}
	for _, q := range []float64{0, 0.5, 0.99, 1} {
		got := h.Quantile(q)
		if got != 0 || math.IsNaN(got) {
			t.Errorf("empty Quantile(%v) = %v, want 0", q, got)
		}
	}
}

// TestHistogramDegenerateInput ensures non-finite / non-positive samples are
// counted and never produce NaN/Inf or a panic.
func TestHistogramDegenerateInput(t *testing.T) {
	h := NewHistogram()
	h.Observe(math.NaN())
	h.Observe(math.Inf(1))
	h.Observe(-5)
	h.Observe(0)
	if h.Count() != 4 {
		t.Fatalf("count = %d, want 4", h.Count())
	}
	for _, q := range []float64{0, 0.5, 1} {
		got := h.Quantile(q)
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Errorf("Quantile(%v) = %v, want finite", q, got)
		}
	}
	// The infinite sample must saturate max at the top boundary, not +Inf.
	if math.IsInf(h.Max(), 0) {
		t.Errorf("max = %v, want finite saturating value", h.Max())
	}
}

// TestHistogramHugeFiniteMaxConsistency guards the Max/Quantile invariant for a
// finite sample above the representable range. Such a value lands in the overflow
// bucket, so Quantile(1.0) reports maxValueMs; Max() must not exceed that or it
// would claim a value the quantiles can never reach. It is clamped to the same
// saturating boundary as +Inf.
func TestHistogramHugeFiniteMaxConsistency(t *testing.T) {
	h := NewHistogram()
	h.Observe(1e9) // finite but >> maxValueMs
	if h.Max() > maxValueMs {
		t.Errorf("Max() = %v, want <= maxValueMs (%v)", h.Max(), maxValueMs)
	}
	if q := h.Quantile(1.0); h.Max() < q {
		t.Errorf("Max() = %v should be >= Quantile(1.0) = %v", h.Max(), q)
	}
	// The saturating boundary is exactly maxValueMs for an over-range sample.
	if h.Max() != maxValueMs {
		t.Errorf("Max() = %v, want %v", h.Max(), maxValueMs)
	}
}

func TestHistogramQuantileClamping(t *testing.T) {
	h := NewHistogram()
	for i := 1; i <= 100; i++ {
		h.Observe(float64(i))
	}
	// Out-of-range q is clamped to [0,1]; q<=0 -> first sample's bucket,
	// q>=1 -> last sample's bucket. Must be finite and ordered.
	lo := h.Quantile(-1)
	hi := h.Quantile(2)
	if lo > hi {
		t.Errorf("Quantile(-1)=%v should be <= Quantile(2)=%v", lo, hi)
	}
}

func TestHistogramConcurrentMergeFanIn(t *testing.T) {
	// Each goroutine fills its own histogram (single-writer per instance), then
	// the main goroutine merges them. This is the intended worker->master flow.
	const workers, each = 16, 2000
	hists := make([]*Histogram, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			h := NewHistogram()
			for i := 0; i < each; i++ {
				h.Observe(float64(1 + (i % 1000)))
			}
			hists[w] = h
		}(w)
	}
	wg.Wait()

	master := NewHistogram()
	for _, h := range hists {
		master.Merge(h)
	}
	if got, want := master.Count(), int64(workers*each); got != want {
		t.Fatalf("merged count = %d, want %d", got, want)
	}
}

func formatQ(q float64) string {
	switch q {
	case 0:
		return "0"
	case 0.1:
		return "10"
	case 0.25:
		return "25"
	case 0.5:
		return "50"
	case 0.75:
		return "75"
	case 0.9:
		return "90"
	case 0.95:
		return "95"
	case 0.99:
		return "99"
	case 1:
		return "100"
	default:
		return "?"
	}
}
