<h1 align="center">tmula</h1>

<p align="center">
  <b>A real-user traffic simulator — find the issues real users would hit, without recruiting them.</b><br>
  Drive virtual users through an explicit <i>behavior graph</i> against your API: they move like
  real people, deviate within the rules, and can swarm a single endpoint — surfacing bugs that
  plain load generation and manual testing miss.
</p>

<p align="center">
  <img src="docs/images/01-flow-map.png" width="840"
       alt="tmula traffic-flow map: virtual users walking a branching shop journey (browse → search / category → product → cart → checkout → done); edge thickness is request volume and red counts mark where the happy path broke">
  <br>
  <sub><i>Live traffic flow from a branching-shop run — edge thickness is request volume, and the red counts mark where the happy path broke (cart / checkout 5xx).</i></sub>
</p>

---

## What is tmula?

Plain load tools fire identical requests at one endpoint. Real users don't — they follow a
journey, branch, hesitate, occasionally go off-script, and pile onto whatever's hot. **tmula**
models that: virtual users walk an explicit **behavior graph** (nodes = API calls, weighted edges
= transitions, dependency edges that are never skipped), and it surfaces issues in three modes:

- **Scenario-following** — does the happy path hold up under realistic, branching traffic?
- **Deviation** — probabilistic skips, step reordering, and payload mutation (never violating a
  dependency) shake out the off-script bugs.
- **Load-concentration** — funnel virtual users onto one API and watch where it degrades.

Observation is **client-side first** (status codes, latency tails, and error / availability /
contract findings); server-side metrics are opt-in. A single Go binary with the web console baked
in runs **locally first** and **scales out** to distributed master/worker mode for large traffic.

> tmula는 **행동 그래프** 기반 실사용자 트래픽 시뮬레이터입니다. 가상 사용자가 실제 사람처럼
> 시나리오를 따라 이동하고, 규칙 안에서 이탈하고, 특정 API에 부하를 집중시켜 — **실제 사용자를
> 모으지 않고도** 평소 부하 도구나 수동 테스트가 놓치는 문제를 찾아냅니다. 단일 Go 바이너리(웹 UI
> 내장)로 로컬에서 바로 돌리고, 필요하면 분산 마스터/워커로 확장합니다.

---

## Quickstart

Requirements: macOS / Linux. (Building from source needs Go 1.25+ and Node 20+.)

**Try it instantly (Docker)** — no Go/Node, no install. One command brings up the console (real UI
baked in) plus both example APIs, each with planted bugs:

```bash
git clone https://github.com/chordpli/tmula.git && cd tmula
docker compose up                    # builds on first run, then starts everything
```

Open <http://localhost:8080>, pick the **shop** or **ticketing** preset, then point it at the bundled
API: set **Base URL** to `http://sample-api:9000` (shop) or `http://ticketing-api:9100` (ticketing) and
add that host (`sample-api` / `ticketing-api`) to the **Allowlist**, then hit **Run**. Inside the
Compose network the engine reaches the SUTs by service name (not `localhost`), so both fields use it.

**Install it** — one line downloads a prebuilt single binary with the web UI baked in:

```bash
curl -fsSL https://raw.githubusercontent.com/chordpli/tmula/main/install.sh | sh
```

Then start the browser console, or run a scenario straight from the CLI:

```bash
tmula --role local --addr :8080      # open http://localhost:8080
tmula run scenario.yaml              # run a scenario, print the findings
```

**Or build from source** — needs Go + Node:

```bash
git clone https://github.com/chordpli/tmula.git && cd tmula
make demo                            # UI + engine + both example APIs, all locally
make web                             # just the console on :8080
# CLI only (fast, placeholder UI):   make build
```

With `make demo` the presets work as-is — they target `localhost:9000` / `:9100`, which the bundled
shop and ticketing APIs serve. Ctrl-C stops all three.

**Or just watch it find bugs** — one command, a sample API with planted bugs:

```bash
./examples/run-demo.sh               # needs go, jq, curl
```

It starts a sample shop API (with deliberate bugs) + the engine, runs an experiment, and prints
the issues it found. See [`examples/`](examples/) for the full walkthrough.

---

## How virtual users behave

| Mode | What it does | Finds |
|------|--------------|-------|
| **Scenario-following** | Users walk the behavior graph by edge weight, honoring dependency edges | Whether the happy path survives realistic, branching traffic |
| **Deviation** | Probabilistic skips, step reordering, payload mutation — never breaking a dependency | Off-script bugs manual testing misses |
| **Load-concentration** | Funnel users onto a single API | Where it degrades or saturates under pressure |

Two workload models drive arrivals: **closed** (a fixed pool of looping users) and **open** (users
arrive at a rate over time — organic concurrency, the realistic default, with optional persona
mixes). A safety layer — host **allowlist** + **rate cap** + **kill switch** — keeps a run from
escaping its target.

---

## Commands

The `tmula` CLI — one binary, no curl/jq, no separately running server:

| Command | What it does |
|---------|--------------|
| `tmula --role local\|master\|worker` | Serve the engine + embedded web console |
| `tmula run <scenario.yaml>` | Run a scenario and print findings — `--users`, `--open <rate> --for <s>`, `--fail-on-findings` (CI gate, exit 2 on issues) |
| `tmula run --target <url> --get\|--post <path>` | Single-endpoint quick run |
| `tmula init --from <openapi.yaml\|session.har>` | Scaffold a scenario from an API spec or HAR recording |

Build & run from source:

| Make target | What it does |
|-------------|--------------|
| `make web` | Build the React UI, embed it, run the console on :8080 |
| `make build` | Go binary only — fast, UI is a placeholder (CLI path) |
| `make dev` | UI hot-reload dev server (proxies `/api` to a running engine) |
| `make test` · `make lint` | Go unit tests · `go vet` + gofmt check |

Health check: <http://localhost:8080/healthz>.

---

## Web console — for PMs & designers, no command line

`make web` builds the React control-plane UI into the binary and serves it at
<http://localhost:8080>. Fill in the target, scenario, and load (virtual users / arrival rate /
personas), hit **Run**, and watch it live:

- a **Traffic flow** map of requests moving across your scenario, with completion / drop-off,
- a **latency heatmap** (time × latency band),
- findings, a standalone **HTML report**, **compare with previous run**, and read-only **share** links,
- a one-click **OpenAPI / HAR import** and scenario **presets**, in a bilingual UI (English / 한국어).

<p align="center">
  <img src="docs/images/02-config-load-model.png" width="480"
       alt="Load-model configuration form: workload (open / closed), arrival rate, duration, max concurrency, think time, and weighted personas">
  <br>
  <sub><i>Dial in the load — an open arrival rate (or a closed pool that loops), think time, and weighted personas, each able to start at a different node.</i></sub>
</p>

<p align="center">
  <img src="docs/images/03-latency-heatmap.png" width="760"
       alt="Latency heatmap: request density by latency band (rows, 0–5 ms up to 5000+ ms) over wall-clock time (columns)">
  <br>
  <sub><i>The latency heatmap — request density by latency band (rows) over time (columns); most requests sit in the 0–10 ms bands with a thin tail.</i></sub>
</p>

> A plain `make build` / `go build` embeds only a placeholder page that tells you to run
> `make web`. The CLI needs no UI build at all.

---

## Example domains

Two complete, runnable demos make it clear how to point tmula at your own API — pick one as a
**preset** in the web console (it fills the scenario *and* the target) or run it from the CLI.

| Example | What it is | Planted bugs it surfaces |
|---------|-----------|--------------------------|
| **shop** — `examples/sample-api` (`:9000`) | A branching store journey: `browse → search / category → product → cart → checkout` | ~8% cart 500s, a checkout that degrades under load, product 404s, a search latency tail |
| **ticketing** — `examples/ticketing-api` (`:9100`) | A concert-seat purchase: `events → detail → seats → hold → pay` | Seat-contention 409s, a payment gateway that buckles in the on-sale rush, sold-out 404s |

Each ships a sample API server, a behavior graph + templates, and an importable **OpenAPI / HAR**
([`examples/imports/`](examples/imports)). Full 0→100 guide: [`examples/USAGE.md`](examples/USAGE.md).

---

## Architecture

A single Go binary (engine + load workers, with an embedded React control-plane UI). Local-first;
scales out to gRPC master/worker for large runs. Client-side observation is the core; server-side
metrics are opt-in.

```
cmd/engine             entrypoint: serve (--role local|master|worker), run, init
internal/domain        core model: experiments, scenario graphs, virtual users, ...
internal/engine        scenario graph execution (dependency edges inviolable)
internal/load          virtual users, load profiles, protocol adapters
internal/workload      open-model (arrival-rate) scheduler + capacity planning
internal/obs           observation collector, finding classification, mergeable summary
internal/safety        allowlist, rate cap, kill switch
internal/store         in-memory (local) + Postgres (distributed) persistence
internal/cluster       gRPC master/worker for distributed runs
internal/pipeline      buffered metric ingest for high-frequency persistence
internal/scenariofile  compact scenario file (YAML/JSON) -> RunSpec
internal/importer      OpenAPI / HAR -> scenario scaffold
internal/report        standalone HTML report + run-to-run comparison
internal/web           embedded React UI
web/                   React + Vite control-plane UI
examples/              sample APIs, scenarios, one-command demo, USAGE guide
```

Design docs — requirements, brief, tech spec, plan — live in
[`context/001-user-traffic-simulator/`](context/001-user-traffic-simulator).

---

## Requirements

- macOS / Linux for the prebuilt binary, **or** Go 1.25+ and Node 20+ to build from source
- `jq` + `curl` for the one-command demo
- Docker + Postgres — optional, only for the distributed-store integration test

## License

TBD.

---

<p align="center">
  Built by <a href="https://github.com/chordpli">chordpli</a>
</p>
