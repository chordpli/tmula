// Command sample-api is a tiny "shop" API used by the tmula examples. It is
// healthy on the happy path but carries a few DELIBERATE bugs so you can see
// the simulator surface them without recruiting real users:
//
//	GET  /browse    healthy, fast
//	GET  /products  healthy, occasionally slow (a latency tail)
//	POST /cart      ~10% return 500 — an intermittent bug devs often miss
//	POST /checkout  saturates under load — flaky, then fully down (503)
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

// checkoutHits counts checkout calls so the endpoint can "saturate" under load.
var checkoutHits atomic.Int64

func main() {
	addr := ":9000"
	if v := os.Getenv("SAMPLE_API_ADDR"); v != "" {
		addr = v
	}

	mux := http.NewServeMux()

	// Happy path: a product listing landing page.
	mux.HandleFunc("GET /browse", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Millisecond)
		writeJSON(w, http.StatusOK, `{"page":"home"}`)
	})

	// Mostly healthy, but ~5% of responses are slow — a latency tail the
	// simulator reflects in p95/p99.
	mux.HandleFunc("GET /products", func(w http.ResponseWriter, _ *http.Request) {
		if rand.Intn(20) == 0 {
			time.Sleep(180 * time.Millisecond)
		} else {
			time.Sleep(6 * time.Millisecond)
		}
		writeJSON(w, http.StatusOK, `{"products":["p1","p2","p3"]}`)
	})

	// BUG: ~10% of normal "add to cart" requests fail with a 500. This is the
	// kind of intermittent error that is easy to miss in manual testing —
	// the simulator flags it as a CONTRACT finding.
	mux.HandleFunc("POST /cart", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		if rand.Intn(10) == 0 {
			writeJSON(w, http.StatusInternalServerError, `{"error":"cart service hiccup"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"cart":"ok"}`)
	})

	// BUG: checkout falls over under load. The first ~25 calls are flaky
	// (~half fail), then the downstream "saturates" and every call returns 503.
	// The simulator flags this as AVAILABILITY (a sustained run of failures =
	// the endpoint is down) and CONTRACT, and it pushes the overall error rate
	// past the threshold — exactly the "what happens when traffic piles up?"
	// question you cannot answer without real load.
	mux.HandleFunc("POST /checkout", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(25 * time.Millisecond)
		n := checkoutHits.Add(1)
		if n > 25 || rand.Intn(2) == 0 {
			writeJSON(w, http.StatusServiceUnavailable, `{"error":"payment downstream unavailable"}`)
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
