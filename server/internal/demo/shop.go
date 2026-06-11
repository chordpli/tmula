// Package demo holds the self-contained assets behind `tmula demo`: a tiny
// "shop" system-under-test with deliberately planted bugs, and the access log
// the demo learns its behavior graph from. Everything is embedded so the demo
// works from a bare installed binary — no checkout, no example files.
package demo

import (
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"
)

// Shop is the demo system-under-test: a miniature store API that is healthy on
// the happy path but carries the same DELIBERATE bugs as the documented example
// SUT, so the demo run surfaces findings the way a real service would:
//
//	GET  /browse    healthy, ~3 ms
//	GET  /search    ~5% of responses sleep ~180 ms — a latency tail (p95/p99)
//	GET  /category  healthy, ~5 ms
//	GET  /product   ~2% return 404 — a rare broken product link (CONTRACT finding)
//	POST /cart      ~8% return 500 — intermittent "cart hiccup" (CONTRACT finding)
//	POST /checkout  the flakiest path: ~8% baseline failures that climb with
//	                concurrent load and are capped at 40% — visibly degraded and
//	                worse under traffic, but never fully down (CONTRACT/threshold)
//
// It mirrors server/examples/sample-api (which is package main and so cannot
// be imported); the planted-bug shapes and probabilities are kept in lockstep
// with that file so the demo and the documented example tell the same story.
type Shop struct {
	// checkoutInflight tracks concurrent checkout requests so the endpoint can
	// saturate under heavy traffic and recover when load eases. It is per
	// instance, so two demos in one process cannot skew each other.
	checkoutInflight atomic.Int64
}

// NewShop returns a fresh demo shop with no in-flight state.
func NewShop() *Shop { return &Shop{} }

// Handler returns the shop's HTTP handler. The endpoints use the package-level
// math/rand source (goroutine-safe), matching the example SUT, so the planted
// failure probabilities hold under any concurrency.
func (s *Shop) Handler() http.Handler {
	mux := http.NewServeMux()

	// Happy path: landing page. Fast, always 200.
	mux.HandleFunc("GET /browse", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Millisecond)
		writeJSON(w, http.StatusOK, `{"page":"home"}`)
	})

	// Mostly healthy, but ~5% of responses are slow — a latency tail the
	// simulator reflects in p95/p99.
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, _ *http.Request) {
		if rand.Intn(20) == 0 {
			time.Sleep(180 * time.Millisecond)
		} else {
			time.Sleep(6 * time.Millisecond)
		}
		writeJSON(w, http.StatusOK, `{"results":["p1","p2","p3"]}`)
	})

	// Healthy category listing. Fast, always 200.
	mux.HandleFunc("GET /category", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		writeJSON(w, http.StatusOK, `{"categories":["electronics","books","clothing"]}`)
	})

	// BUG: ~2% of product detail requests return 404 — a rare broken product
	// link. The simulator flags this as a CONTRACT finding.
	mux.HandleFunc("GET /product", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(6 * time.Millisecond)
		if rand.Intn(50) == 0 {
			writeJSON(w, http.StatusNotFound, `{"error":"product not found"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"product":"p7","price":42}`)
	})

	// BUG: ~8% of "add to cart" requests fail with a 500. This is the kind of
	// intermittent error that is easy to miss in manual testing — the simulator
	// flags it as a CONTRACT finding.
	mux.HandleFunc("POST /cart", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		if rand.Intn(100) < 8 {
			writeJSON(w, http.StatusInternalServerError, `{"error":"cart service hiccup"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"cart":"ok"}`)
	})

	// BUG: checkout DEGRADES under concurrent load but never fully falls over,
	// and RECOVERS when traffic eases — a realistic "flaky under pressure"
	// payment path, not a permanent outage. Failure probability rises gently
	// with the number of in-flight checkout calls (+2% per call) from an ~8%
	// baseline, capped at 40%: it always shows up as the problem area and
	// visibly worsens under traffic, but never reads as fully down.
	mux.HandleFunc("POST /checkout", func(w http.ResponseWriter, _ *http.Request) {
		n := s.checkoutInflight.Add(1)
		defer s.checkoutInflight.Add(-1)
		time.Sleep(15 * time.Millisecond)
		failProb := 0.08 + float64(n)*0.02
		if failProb > 0.4 {
			failProb = 0.4
		}
		if rand.Float64() < failProb {
			writeJSON(w, http.StatusServiceUnavailable, `{"error":"payment downstream slow"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"order":"placed"}`)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"status":"ok"}`)
	})

	return mux
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
