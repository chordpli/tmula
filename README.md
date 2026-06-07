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
cmd/engine           entrypoint: serve (--role local|master|worker), run, init
internal/domain      core model: experiments, scenario graphs, virtual users, ...
internal/engine      scenario graph execution (dependency edges inviolable)
internal/load        virtual users, load profiles, protocol adapters
internal/workload    open-model (arrival-rate) scheduler + capacity planning
internal/obs         observation collector, finding classification, mergeable summary
internal/safety      allowlist, rate cap, kill switch
internal/store       in-memory (local) + Postgres (distributed) persistence
internal/cluster     gRPC master/worker for distributed runs
internal/pipeline    buffered metric ingest for high-frequency persistence
internal/scenariofile  compact scenario file (YAML/JSON) -> RunSpec
internal/importer    OpenAPI / HAR -> scenario scaffold
internal/report      standalone HTML report + run-to-run comparison
internal/web         embedded React UI
web/                 React + Vite control-plane UI
examples/            sample API, scenario, one-command demo, USAGE guide
```

## Quick start — the `tmula run` CLI

One binary, one command — no curl, no jq, no separately running server:

```bash
go build -o ./bin/tmula ./cmd/engine

./bin/tmula run --target http://localhost:9000 --get /health --users 20  # one endpoint
./bin/tmula run examples/shop/scenario.yaml --users 80                   # a whole scenario
./bin/tmula init --from openapi.yaml --out scenario.yaml                 # scaffold from a spec
```

It boots an in-process engine, runs the experiment, and prints the findings.
`--fail-on-findings` turns it into a CI gate (exit 2 on issues). See
[`examples/USAGE.md`](examples/USAGE.md) for the full 0→100 guide.

## Full demo (sample API with planted bugs)

```bash
./examples/run-demo.sh
```

Starts a sample shop API (with a few deliberate bugs) and the engine, runs an
experiment, and prints the issues it found — see [`examples/`](examples/) for
details and how to point it at your own API. Requires `go`, `jq`, `curl`.

## Requirements

- Go 1.25+
- Node 20+ (only to build the web UI)
- Docker + Postgres (optional — only for the distributed store integration test)

## Web console (for PMs, designers — no command line)

The control-plane UI runs in the browser. One command builds the React UI into
the binary and starts it:

```bash
make web      # build the UI, embed it, run the engine on :8080
```

Then open <http://localhost:8080>: fill in the target, scenario and load
(virtual users / arrival rate / personas), hit **Run**, watch live progress, and
read the findings — with **View HTML report**, **Compare with previous run**, and
read-only **share** links.

> A plain `make build` / `go build` embeds only a lightweight placeholder page
> (which just tells you to run `make web`). The CLI needs no UI build at all.

## Build & run

```bash
make web          # build UI + embed + run the browser console on :8080  (web path)
make build        # Go binary only — fast, UI is a placeholder (CLI path)
make run          # build + run the engine on :8080 (placeholder UI)
make dev          # UI hot-reload dev server (proxies /api to a running engine)
make test         # Go unit tests
make lint         # go vet + gofmt check
```

Health check: <http://localhost:8080/healthz>.

## License

TBD.
