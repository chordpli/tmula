# tmula examples

A runnable example so you can see how tmula is used — point it at an API, send
virtual users through a behavior graph, and read the issues it finds **without
recruiting real users**.

## What's here

| Path | What it is |
|---|---|
| `sample-api/` | A tiny "shop" API (stdlib Go) with a few **deliberate bugs** to find |
| `shop/graph.json` | The behavior graph: `browse → products → cart → checkout` (checkout *depends on* cart) |
| `shop/templates.json` | The API request templates each node calls |
| `run-demo.sh` | One command: starts everything, runs an experiment, prints the findings |

## Quick start (one command)

```bash
./examples/run-demo.sh          # or: ./examples/run-demo.sh 100   (100 virtual users)
```

Requires `go`, `jq`, `curl`. It builds tmula, starts the sample API on `:9000`
and the engine on `:8080`, runs the `shop` scenario, prints the report, and
cleans up.

### What you'll see

The sample API is healthy on the happy path but has planted bugs:

- `POST /cart` fails ~10% of the time (an intermittent 500)
- `POST /checkout` is flaky, then **falls over under load** and stays down (503)
- `GET /products` has an occasional slow response (a latency tail)

So the run reports something like:

```
• [CRITICAL] contract: 8 contract violation(s) on cart (unexpected error on the happy path)
• [CRITICAL] contract: 90 contract violation(s) on checkout (unexpected error on the happy path)
• [CRITICAL] availability: 76 consecutive failures on checkout (saturation or downtime)
• [WARNING]  threshold: error rate 0.24 exceeded threshold 0.20
```

That's the point: a developer/PM/designer finds the cart hiccup, the checkout
that **collapses under traffic**, and the latency tail **before** real users
hit them (exact counts vary per run).

## Drive it from the UI instead

```bash
go run ./examples/sample-api &           # sample API on :9000
go run ./cmd/engine --role local &       # engine + UI on :8080
open http://localhost:8080
```

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
