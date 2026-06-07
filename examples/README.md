# tmula examples

A runnable example so you can see how tmula is used — point it at an API, send
virtual users through a behavior graph, and read the issues it finds **without
recruiting real users**.

## What's here

| Path | What it is |
|---|---|
| [`USAGE.md`](USAGE.md) | **Full 0→100 usage guide** — REST API, open (arrival-rate) model, personas, distributed runs |
| `sample-api/` | A tiny **"shop"** API (stdlib Go) with a few **deliberate bugs** to find (:9000) |
| `shop/{graph,templates}.json`, `shop/scenario.yaml` | The shop journey (`browse → search/category → product → cart → checkout`) with exit drop-offs |
| `ticketing-api/` | A second tiny API: **concert ticketing** — seat-contention 409s, a payment rush, sold-out 404s (:9100) |
| `ticketing/{graph,templates}.json`, `ticketing/scenario.yaml` | The ticketing journey (`events → detail → seats → hold → pay`) with exit drop-offs |
| `imports/` | Importable **OpenAPI + HAR** samples for both domains (web UI → *Import from OpenAPI / HAR*, or `tmula init --from`) |
| `run-demo.sh` | One command: starts everything, runs an experiment, prints the findings |

### Two example domains

There are two complete demos so it's clear how to swap tmula onto your own API —
a **shop** and a **concert-ticketing** site. Each ships a sample API server, a
behavior graph + templates, and an importable OpenAPI/HAR. In the web console
they're one-click **presets** ("Branching shop" / "Concert tickets"): picking one
fills the scenario *and* points the target at that API's port. Run the matching
server first:

```bash
go run ./examples/sample-api      # shop       → :9000
go run ./examples/ticketing-api   # ticketing  → :9100
```

## Easiest: the `tmula run` CLI

One binary, one command — no curl, no jq, no separately running server:

```bash
go build -o ./bin/tmula ./cmd/engine

# a single endpoint
./bin/tmula run --target http://127.0.0.1:9000 --get /browse --users 30

# a whole scenario from one compact file (see shop/scenario.yaml)
./bin/tmula run examples/shop/scenario.yaml --users 80

# an organic, arrival-rate (open) load
./bin/tmula run examples/shop/scenario.yaml --open 200 --for 30

# scaffold a scenario from an existing OpenAPI spec or HAR recording
./bin/tmula init --from openapi.yaml --out scenario.yaml

# gate CI on results (exit 2 if findings; --fail-on-severity critical to narrow)
./bin/tmula run examples/shop/scenario.yaml --users 80 --fail-on-findings
```

It boots an in-process engine, runs the experiment, and prints the findings.
`--json` emits the raw report; `--engine http://host:8080` targets a running
engine instead. See [`USAGE.md`](USAGE.md) for the full guide.

## Quick start (one command)

```bash
./examples/run-demo.sh          # or: ./examples/run-demo.sh 100   (100 virtual users)
```

Requires `go`, `jq`, `curl`. It builds tmula, starts the sample API on `:9000`
and the engine on `:8080`, runs the `shop` scenario, prints the report, and
cleans up.

### What you'll see

The sample API is healthy on the happy path but has planted bugs:

- `GET /search` has an occasional slow response (~5% tail latency, ~180 ms)
- `GET /product` returns 404 ~2% of the time (broken product link)
- `POST /cart` fails ~8% of the time (an intermittent 500)
- `POST /checkout` **saturates under concurrent load** but **recovers when traffic drops** — unlike a permanent outage

So the run reports something like:

```
• [CRITICAL] contract: 6 contract violation(s) on product (unexpected 404 on the happy path)
• [CRITICAL] contract: 8 contract violation(s) on cart (unexpected error on the happy path)
• [CRITICAL] contract: 90 contract violation(s) on checkout (unexpected error on the happy path)
• [CRITICAL] availability: 53 consecutive failures on checkout (saturation or downtime)
• [WARNING]  threshold: error rate 0.24 exceeded threshold 0.20
```

That's the point: a developer/PM/designer finds the broken product links, the
cart hiccup, and the checkout that **saturates under traffic but recovers**
**before** real users hit them (exact counts vary per run).

## Drive it from the UI instead

```bash
go run ./examples/sample-api &   # sample API on :9000
make web                         # build the UI into the binary + run on :8080
open http://localhost:8080
```

> `make web` is the one command for the browser console. A plain `go build`
> embeds only a placeholder page; use `make dev` for UI hot-reload against a
> running engine.

In the form, set **Target base URL** = `http://127.0.0.1:9000`, **Allowlist** =
`127.0.0.1`, paste `shop/graph.json` and `shop/templates.json`, **Start node** =
`browse`, then **Run**. Watch live progress, then read the findings. Click
**share** (or `POST /api/runs/{id}/share`) for a read-only viewer link
(`/?share=<token>`) you can send to a teammate.

## Run it distributed (optional)

Start one or more workers, then put their addresses in the **Worker addresses**
field (or `--workers`):

```bash
go run ./cmd/engine --role worker --addr :9101 &
go run ./cmd/engine --role worker --addr :9102 &
go run ./cmd/engine --role local  --addr :8080 --workers 127.0.0.1:9101,127.0.0.1:9102 &
```

The master fans the virtual users across the workers and aggregates the
findings identically.

## Adapt it to your own API

1. Point `targetEnv.baseUrl` at your service and add its host to `allowlist`
   (only dev/staging hosts are allowed — prod is locked).
2. Edit `shop/templates.json` with your real endpoints (method, path, headers,
   `payloadTemplate`). Use `{{.token}}` / `{{.subject}}` in headers/payloads to
   inject per-user credentials.
3. Edit `shop/graph.json` to describe how a real user moves between them. Mark
   required-order edges with `"dependency": true` — those steps are never
   skipped, even when a virtual user deviates.
4. Re-run. The findings now describe *your* service.
