// Command ticketing-api is a tiny "concert tickets" API used by the tmula
// examples — a second domain alongside the shop (examples/sample-api). It is
// healthy on the happy path but carries DELIBERATE bugs typical of a high-demand
// on-sale, so the simulator surfaces them without recruiting real buyers:
//
//	GET  /events       list of shows — healthy, fast
//	GET  /events/{id}  one show's detail — ~3% return 404 (sold out / removed)
//	GET  /seats        seat availability — ~6% slow (a popular show; latency tail)
//	POST /hold         hold a seat — ~18% return 409 (someone grabbed it first:
//	                   the classic seat-contention race) → CONTRACT finding
//	POST /pay          pay for held seats — degrades under the on-sale rush
//	                   (failure climbs with concurrent load, capped at 40%)
//
// Run it:  go run ./examples/ticketing-api   (listens on :9100)
package main

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// payInflight tracks concurrent payments so the gateway can "rush-degrade".
var payInflight atomic.Int64

func main() {
	addr := ":9100"
	if v := os.Getenv("TICKETING_API_ADDR"); v != "" {
		addr = v
	}

	mux := http.NewServeMux()

	// Happy path: the list of upcoming shows.
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Millisecond)
		writeJSON(w, http.StatusOK, `{"events":["e7","e8","e9"]}`)
	})

	// BUG: ~3% of show-detail reads 404 — a show that just sold out or was pulled.
	mux.HandleFunc("GET /events/{id}", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		if rand.Intn(100) < 3 {
			writeJSON(w, http.StatusNotFound, `{"error":"show not found"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"event":"e7","title":"Live in Seoul"}`)
	})

	// Mostly healthy, but ~6% of seat-map reads are slow — a popular show whose
	// availability query strains the backend (a p95/p99 latency tail).
	mux.HandleFunc("GET /seats", func(w http.ResponseWriter, _ *http.Request) {
		if rand.Intn(100) < 6 {
			time.Sleep(150 * time.Millisecond)
		} else {
			time.Sleep(6 * time.Millisecond)
		}
		writeJSON(w, http.StatusOK, `{"available":["A12","A13","B4"]}`)
	})

	// BUG: ~18% of holds lose the race — another buyer grabbed the seat first, so
	// the hold returns 409 Conflict. This seat contention is the signature
	// ticketing failure under load; the simulator flags it as a CONTRACT finding.
	mux.HandleFunc("POST /hold", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(8 * time.Millisecond)
		if rand.Intn(100) < 18 {
			writeJSON(w, http.StatusConflict, `{"error":"seat already held"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"hold":"h-882","expiresIn":120}`)
	})

	// BUG: the payment gateway degrades under the on-sale rush. Failure climbs with
	// concurrent load and is capped at 40%, so it is flaky-under-pressure (not an
	// outage) and recovers when the rush subsides — the same shape as the shop's
	// checkout. The simulator flags the elevated error rate / contract misses.
	mux.HandleFunc("POST /pay", func(w http.ResponseWriter, _ *http.Request) {
		n := payInflight.Add(1)
		defer payInflight.Add(-1)
		time.Sleep(15 * time.Millisecond)
		failProb := 0.08 + float64(n)*0.02
		if failProb > 0.4 {
			failProb = 0.4
		}
		if rand.Float64() < failProb {
			writeJSON(w, http.StatusServiceUnavailable, `{"error":"payment gateway busy"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"order":"o-1042","tickets":2}`)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"status":"ok"}`)
	})

	log.Printf("ticketing-api (concert seats) listening on %s", addr)
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
