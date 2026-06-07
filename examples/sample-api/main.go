// Command sample-api is a tiny "shop" API used by the tmula examples. It is
// healthy on the happy path but carries several DELIBERATE bugs so you can see
// the simulator surface them without recruiting real users:
//
//	GET  /browse    healthy, ~3 ms
//	GET  /search    ~5% of responses sleep ~180 ms — a latency tail (p95/p99)
//	GET  /category  healthy, ~5 ms
//	GET  /product   ~2% return 404 — a rare broken product link (CONTRACT finding)
//	POST /cart      ~8% return 500 — intermittent "cart hiccup" (CONTRACT finding)
//	POST /checkout  load-proportional failure that RECOVERS when load drops —
//	                saturation under heavy traffic, not a permanent outage
//	                (AVAILABILITY/CONTRACT finding under load)
//
// Run it:  go run ./examples/sample-api   (listens on :9000)
package main

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// checkoutInflight tracks the number of concurrent checkout requests so the
// endpoint can saturate under heavy traffic and recover when load eases.
var checkoutInflight atomic.Int64

func main() {
	addr := ":9000"
	if v := os.Getenv("SAMPLE_API_ADDR"); v != "" {
		addr = v
	}

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

	// BUG: checkout saturates under concurrent load but RECOVERS when traffic
	// drops — unlike a permanent outage. Failure probability climbs with the
	// number of in-flight checkout calls and falls when load eases, so the
	// simulator flags AVAILABILITY/CONTRACT under heavy traffic but the endpoint
	// returns to health once the concurrency subsides.
	mux.HandleFunc("POST /checkout", func(w http.ResponseWriter, _ *http.Request) {
		n := checkoutInflight.Add(1)
		defer checkoutInflight.Add(-1)
		time.Sleep(25 * time.Millisecond)
		// n<=6 ≈ healthy; n>=20 ≈ always fails; scales linearly between.
		failProb := (float64(n) - 6) / 14.0
		if failProb > 0 && rand.Float64() < failProb {
			writeJSON(w, http.StatusServiceUnavailable, `{"error":"payment downstream saturated"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"order":"placed"}`)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"status":"ok"}`)
	})

	log.Printf("sample-api (shop) listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
