# tmula

A real-user **traffic simulator**. Instead of plain load generation, tmula
drives many virtual users through an explicit **behavior graph** — they move
like real people, occasionally deviate (probabilistic skips, reordering and
payload mutation, but never violating dependency edges), and can be funneled to
hammer a specific API. It surfaces issues in three modes — scenario-following,
deviation, and load-concentration — so developers, PMs and designers can find
problems **without recruiting real users**.

**👉 New here? See [`examples/`](examples/)** — one command starts a sample API
with planted bugs and shows tmula finding them. Design docs (requirements,
brief, tech spec, issue breakdown) live in `context/001-user-traffic-simulator/`.

## Architecture (one-liner)

A single Go binary (engine + load workers, with an embedded React control-plane
UI) that runs **locally first** and **scales out** to distributed master/worker
mode for large traffic. Client-side observation is the core; server-side
metrics are opt-in.

```
cmd/engine        entrypoint (--role local|master|worker)
internal/domain   core model: experiments, scenario graphs, virtual users, ...
internal/engine   scenario graph execution (dependency edges inviolable)
internal/load     virtual users, load profiles, protocol adapters
internal/obs      observation collector + finding classification
internal/safety   allowlist, rate cap, kill switch
internal/store    in-memory (local) + Postgres (distributed) persistence
internal/cluster  gRPC master/worker for distributed runs
internal/pipeline buffered metric ingest for high-frequency persistence
internal/web      embedded React UI
web/              React + Vite control-plane UI
examples/         sample API + scenario + one-command demo
```

## Quick demo

```bash
./examples/run-demo.sh
```

Starts a sample shop API (with a few deliberate bugs) and the engine, runs an
experiment, and prints the issues it found — see [`examples/`](examples/) for
details and how to point it at your own API. Requires `go`, `jq`, `curl`.

## Requirements

- Go 1.23+
- Node 20+ (only to build the web UI)
- Docker + Postgres (optional — only for the distributed store integration test)

## Build & run

```bash
make build        # build the single binary (embeds the UI placeholder)
make test         # run Go unit tests
make lint         # go vet + gofmt check
make run          # run a local engine on :8080

make web-build    # build the React UI
make embed        # build the UI, embed it, then build the binary
```

Then open <http://localhost:8080> (health: <http://localhost:8080/healthz>).

## License

TBD.
