<h1 align="center">tmula</h1>

<p align="center">
  <b>
    See where your user flow breaks first under load - feed it an access log
    and it learns the journey for you.
  </b><br>
  tmula turns real traffic into an explicit <i>behavior graph</i>, then drives virtual users
  through it - branching, hesitating, sometimes off-script, swarming a single endpoint - and tells
  you not just <i>that</i> something failed but <i>where in the journey</i> it failed and
  <i>whether load or a real bug</i> caused it.
</p>

<p align="center">
  🌐 <b>README</b>:
  <a href="README.md">English</a> · <a href="README.ko.md">한국어</a><br>
  📖 <b>User manual</b> - the full guide (concepts, every JSON field, CLI, findings, FAQ):
  <a href="docs/guide.en.md">English</a> · <a href="docs/guide.ko.md">한국어</a>
</p>

<p align="center">
  <img src="docs/images/demo-live.gif" width="840"
       alt="tmula web console during a live run">
  <br>
  <sub><i>
    The web console during a live run - requests stream across the behavior graph
    while the latency heatmap fills in.
  </i></sub>
</p>

---

## What is tmula?

Most load tools answer "how many requests per second can it take?" tmula answers a different one:
**when traffic flows the way your users actually move, where does the flow break first - and is that
breakage load, or a real bug?**

The fastest way in is an access log. Point tmula at one and it *learns* the user journey - which
endpoints follow which, how often, how fast - as an explicit **behavior graph**: nodes = API calls,
weighted edges = transitions, and dependency edges that are never skipped. (No log? Scaffold the
graph from an OpenAPI spec or a HAR, or draw it by hand.) Then it sends virtual traffic through that
graph and watches where the flow slows down, breaks, or concentrates.

Virtual users follow a journey, branch, hesitate, sometimes go off-script, and pile onto
high-traffic endpoints. It surfaces issues in three modes:

- **Scenario-following** - does the happy path hold up under realistic, branching traffic?
- **Deviation** - a configurable per-step probability that a user goes off-script: it abandons
  the journey mid-flow or wanders onto an unlikely transition, never violating a dependency -
  shaking out the off-script bugs.
- **Load-concentration** - aim a whole run at a single endpoint (`tmula run --get /path`), or
  spike the open-model arrival rate, and watch where it degrades.

When a flow does break, tmula doesn't stop at the number. It can replay a failing session in
isolation to tell a **load-dependent** failure from a **functional** one, then gate the run against
a baseline so CI blocks only *new* findings.

The generated traffic is a **dependency-safe approximation** of the learned distribution, not a
replay. At each step the walker re-normalizes the learned weights over only the *eligible* next
steps - those whose dependencies are already satisfied - and the node cap folds rare endpoints into
bridged transitions. The shape of the journey is preserved while the hard preconditions (no checkout
before cart) always hold: a trade tmula makes on purpose.

(Payload mutation, step reordering, and time-shaped concentration profiles are built but not yet
wired into a run - see the [Roadmap](#roadmap).)

Observation is **client-side first** (status codes, latency tails, and error / availability /
contract findings); server-side metrics are opt-in. A single Go binary with the web console baked
in runs **locally first** and **scales out** to distributed master/worker mode for large traffic.

tmula is **not** a replacement for mature load-testing suites - k6, Locust, JMeter, Gatling,
Artillery, and nGrinder cover scripting, distributed execution, dashboards, and CI in far more
depth. It starts from the narrower angle above, and leans on the same foundations.

---

## Quickstart

Requirements: macOS / Linux. (Building from source needs Go 1.25+ and Node 20+.)

**Fastest - install one line, run one command, read real findings in ~3 minutes:**

```bash
curl -fsSL https://raw.githubusercontent.com/chordpli/tmula/main/install.sh | sh
tmula demo
```

`tmula demo` runs the whole loop self-contained - no config file, no second terminal:

1. Boots a tiny shop API with planted bugs: a flaky cart, a checkout that degrades
   under load, and a rare broken product link.
2. **Learns** a behavior graph from that shop's access log.
3. Starts the engine + web console (default `:8080`, change with `--addr`), opens the
   browser straight into the run's live view (`/?run=<run-id>`), and replays the learned traffic
   against the shop for `--duration` (default 60s).
4. Prints the findings summary plus concrete next steps: a ready-to-paste
   `tmula reproduce` command, the run's HTML report URL, and the `tmula init` / `tmula run` pair
   that points the same loop at your own service.

It stays up until Ctrl-C so those commands keep working; `--no-browser` skips opening the console.

> The browser console page needs a binary with the embedded UI - the install script's prebuilt
> binary and the Docker image ship it. With a plain `go build` the demo still works end to end;
> either way, the terminal summary and `report.html` link still show the results.

**Try it with Docker** - no Go/Node, no install. One command brings up the console (real UI
baked in) plus both example APIs, each with planted bugs:

```bash
git clone https://github.com/chordpli/tmula.git && cd tmula
docker compose up                    # builds on first run, then starts everything
```

Open <http://localhost:8080>, pick the **shop** or **ticketing** preset, then point it at the
bundled API: set **Base URL** to `http://sample-api:9000` (shop) or `http://ticketing-api:9100`
(ticketing) and add that host (`sample-api` / `ticketing-api`) to the **Allowlist**, then hit
**Run**. Inside the Compose network the engine reaches the example APIs by service name
(not `localhost`), so both fields use it.

**Run it for real** - the same installed binary serves the console or runs scenarios:

```bash
tmula --role local --addr :8080      # open http://localhost:8080
tmula run scenario.yaml              # run a scenario, print the findings
```

**Or build from source** - needs Go + Node:

```bash
git clone https://github.com/chordpli/tmula.git && cd tmula
make demo                            # UI + engine + both example APIs, all locally
make web                             # just the console on :8080
# CLI only (fast, placeholder UI):   make build
```

With `make demo` the presets work as-is - they target `localhost:9000` / `:9100`, which the bundled
shop and ticketing APIs serve. Ctrl-C stops all three.

Prefer a demo script you can read end to end? [`examples/run-demo.sh`](examples/) is the manual
version of `tmula demo` (explicit curl/jq calls; needs `go`, `jq`, `curl`) - see
[`examples/`](examples/) for the full walkthrough.

---

## How virtual users behave

| Feature | What it does | Status |
|---------|--------------|--------|
| **Scenario-following** | Follows edge weights and honors dependency edges | ✅ Works |
| **Deviation** | Uses `deviationRate` for off-script paths; dependencies still hold | ✅ Works |
| **Load-concentration** | Targets one endpoint or spikes open arrivals | ✅ Works |
| **Think time** | Adds a random pause between user steps | ✅ Works |
| **Findings thresholds** | Tunes error-rate, p95, and availability gates | ✅ Works |
| **Authenticated runs** | Gives each virtual user a real credential — token pool, live login, self-signed JWT, session cookie | ✅ Works |
| **Payload mutation** | Mutates bodies to surface input-validation bugs | 🚧 [Roadmap](#roadmap) |
| **Step reordering** | Visits permitted steps out of scripted order | 🚧 [Roadmap](#roadmap) |
| **Concentration profiles** | Applies timed concurrency to one graph node | 🚧 [Roadmap](#roadmap) |

Two workload models drive arrivals: **closed** (a fixed pool of looping users) and **open** (users
arrive at a rate over time, for organic concurrency). Open is the realistic default and takes an
optional persona mix. A safety layer (host **allowlist**, **rate cap**, **kill switch**) keeps a run
from escaping its target.

Traffic can carry **real auth**: an `auth:` block assigns every virtual user a credential — a
pre-issued token pool, a real login per user (with mid-run refresh), a self-signed JWT when you hold
the key, a session cookie, or an OAuth2 IdP via the web console's guided mode. Secrets never cross
the wire:

```yaml
auth:
  strategy: login
  source: { file: users.csv, format: csv }   # one row per account (username,password)
  login:
    flow:
      - id: signin
        request: POST /auth/token
        body: '{"username":"{{.username}}","password":"{{.password}}"}'
```

Strategy choices, worked examples, and the decision table live in
[Authenticated runs](docs/guide.en.md#authenticated-runs) in the manual.

---

## Response data correlation

Later steps can reuse values returned by earlier steps. Add an `extract` map to a request-bearing
step (or API template): keys become session variables, values are JSON paths in the response body.
Those variables are then available in later `path`, `headers`, and `payloadTemplate` fields via
Go template syntax.

```yaml
target: http://localhost:9000
flow:
  - id: products
    request: GET /products
    extract:
      productId: items.0.id
  - id: cart
    request: POST /cart
    body: '{"productId":"{{.productId}}","qty":1}'
```

Each virtual user/session keeps its own extracted variables, so one user's product/cart IDs do not
bleed into another user's journey.

---

## Findings that carry their evidence

A finding is no longer just a sentence. Each per-endpoint finding ships an **evidence bundle**:
up to 5 representative failing sessions (the earliest occurrences plus the slowest of the rest),
each with its session ID (the `X-Tmula-Session-ID` header value to grep your server logs for),
its **seed coordinates** (run seed + user index = session seed), its persona, and the graph path
it walked into the failure - plus the finding's status-code distribution and where in the run
window the occurrences clustered. The web console and the HTML report render it as a collapsible
panel per finding.

**Reproduce - real bug, or load artifact?** Those seed coordinates make a finding *replayable*:
`tmula reproduce` re-runs one evidence session alone (no concurrent load) and classifies the
root cause from how often the failure recurs:

```bash
tmula reproduce --engine http://localhost:8080 --run run-12 --finding contract/checkout
```

```
Reproduce contract/checkout — run run-12
  session u17  seed=18 (run seed 1 + user index 17)
  original failure path: browse → search → product → cart → checkout

Attempts (3, single session, no concurrent load):
  #1  not reproduced  browse:200(3ms) search:200(5ms) product:200(4ms) cart:200(6ms) checkout:200(9ms)
  #2  not reproduced  browse:200(3ms) search:200(4ms) product:200(4ms) cart:200(5ms) checkout:200(8ms)
  #3  not reproduced  browse:200(2ms) search:200(5ms) product:200(4ms) cart:200(6ms) checkout:200(8ms)

Verdict: load-dependent — reproduced 0/3 attempts without load → likely concurrency or saturation
```

- **functional** - the failure reproduced on *every* isolated attempt: it does not need load, so
  it is likely a plain functional bug. Fix the code path.
- **load-dependent** - it reproduced on *no* attempt: it likely needs the original concurrency or
  saturation. Look at pools, locks, capacity.
- **flaky** - it reproduced on some attempts only.

The verdict is stamped onto the stored finding (`rootCauseClass`) and shows in later reports. It
is a *signal, not a proof*: the replay recreates the session's traffic composition (same seed,
same walk), never the original timing or target state.

**Baseline gate - fail CI only on what *this* change broke.** `tmula run --baseline-file
main-report.json` (or `--baseline <run-id> --engine <url>`) diffs the findings against a previous
run by their stable identity and exits `3` only when **new** findings appear - known, persisting
problems do not block every PR. A `--known-issues issues.yaml` file suppresses accepted findings,
each entry with a mandatory `reason` and `expires` date so nothing is silenced forever. The
verdict (new / resolved / persisting / suppressed) lands in the terminal output and the GitHub
Actions step summary. Full reference: [the user manual](docs/guide.en.md#the-cli).

---

## Commands

The `tmula` CLI - one binary, no curl/jq, no separately running server:

- `tmula demo`: the whole loop in one command. It boots a shop with planted bugs, **learns** its
  behavior graph from an access log, replays the learned traffic, and prints findings plus next
  steps. Options: `--addr :8080`, `--duration 60s`, `--no-browser`.
- `tmula --role local|master|worker`: serve the engine + embedded web console.
- `tmula run <scenario.yaml>`: run a scenario and print findings. Key options include `--users`,
  `--open <rate> --for <s>`, `--fail-on-findings`, `--baseline <run-id>`,
  `--baseline-file <report.json>`, `--known-issues <yaml>`, and `--summary`.
- `tmula run --target <url> --get|--post <path>`: single-endpoint quick run.
- `tmula reproduce --engine <url> --run <id> --finding <category/ref>`: replay one finding's
  evidence session alone (no load) and classify it `functional` / `load-dependent` / `flaky`.
- `tmula init --from <openapi.yaml|session.har|access.log>`: scaffold a scenario from an API spec,
  HAR recording, or access log. Log formats are auto-detected: nginx/Apache combined, JSON lines,
  AWS ALB, CloudFront, Caddy, and Traefik.

Use the bundled **GitHub Action** (`uses: chordpli/tmula@main`) to gate merges. It installs the
binary, runs the scenario, and posts the findings summary on the workflow page and optionally on
the PR. See [Running in CI](docs/guide.en.md#running-in-ci).

Build & run from source:

| Make target | What it does |
|-------------|--------------|
| `make web` | Build the React UI, embed it, run the console on :8080 |
| `make build` | Go binary only - fast, UI is a placeholder (CLI path) |
| `make demo` | Engine + **both** example SUTs (shop :9000 · ticketing :9100) |
| `make shop` · `make ticketing` | Run **one** example SUT on its own — shop :9000 / ticketing :9100 (override the port with `SAMPLE_API_ADDR=:PORT` / `TICKETING_API_ADDR=:PORT`) |
| `make dev` | UI hot-reload dev server (proxies `/api` to a running engine) |
| `make test` · `make lint` | Go unit tests · `go vet` + gofmt check |

Health check: <http://localhost:8080/healthz>.

---

## Drive it from Claude Code (skills)

If you use [Claude Code](https://docs.claude.com/en/docs/claude-code), this repo ships a suite of
**skills** that take you from an API to triaged findings conversationally — no need to remember the
commands above. Open the repo in Claude Code and just say what you want, or run the orchestrator:

```
/tmula-up http://your-api        # or a Swagger/OpenAPI URL, a HAR, or an access log
```

It walks **scaffold → enrich → run → triage**: discovers the spec from the URL (if the API serves
one), writes a `json/scenario.json`, makes it runnable and safe, load-tests it behind a
**non-production safety gate**, and classifies what broke — stopping to confirm before sending
traffic. The four stages are also standalone skills (`tmula-scaffold` / `tmula-enrich` / `tmula-run`
/ `tmula-triage`). A guard hook blocks a run against a non-loopback host unless you opt in.

**Skills docs:** overview [`docs/skills.md`](docs/skills.md) · full guide
([English](docs/skills-guide.md) · [한국어](docs/skills-guide.ko.md)) · hands-on walkthrough
([English](docs/skills-tutorial.md) · [한국어](docs/skills-tutorial.ko.md)).

---

## Web console - for PMs & designers, no command line

`make web` builds the React control-plane UI into the binary and serves it at
<http://localhost:8080>. Fill in the target, scenario, and load (virtual users / arrival rate /
personas / deviation rate), hit **Run**, and watch it live:

- a **Traffic flow** map of requests moving across your scenario, with completion / drop-off,
- a **latency heatmap** (time × latency band),
- findings, each with a collapsible **evidence panel** (representative sessions with seed
  coordinates, status-code and timing distributions), a standalone **HTML report**, **compare
  with previous run**, and read-only **share** links,
- opt-in **server metrics**: Prometheus series fetched over the run's window, shown beside the
  client-side stats,
- a one-click **OpenAPI / HAR / access-log import** and scenario **presets**, in a bilingual UI
  (English / 한국어). Logs go further: the branching graph is *learned* from real traffic, and the
  import reports its **coverage** - how many lines were used, skipped, and why.

<p align="center">
  <img src="docs/images/01-flow-map.png" width="840"
       alt="tmula traffic-flow map for a branching shop journey">
  <br>
  <sub><i>
    The traffic-flow map from a branching-shop run - edge thickness is request volume,
    and red counts mark where the happy path broke.
  </i></sub>
</p>

<p align="center">
  <img src="docs/images/02-config-load-model.png" width="480"
       alt="Load-model configuration form">
  <br>
  <sub><i>
    Dial in the load - open arrival rate or a closed pool, think time, and weighted personas.
  </i></sub>
</p>

<p align="center">
  <img src="docs/images/03-latency-heatmap.png" width="760"
       alt="Latency heatmap by time and latency band">
  <br>
  <sub><i>
    The latency heatmap - request density by latency band over time.
  </i></sub>
</p>

> A plain `make build` / `go build` embeds only a placeholder page that tells you to run
> `make web`. The CLI needs no UI build at all.

---

## Example domains

Two complete, runnable demos make it clear how to point tmula at your own API - pick one as a
**preset** in the web console (it fills the scenario *and* the target) or run it from the CLI.

- **shop** - `server/examples/sample-api` (`:9000`)
  - Journey: `browse → search / category → product → cart → checkout`
  - Planted bugs: ~8% cart 500s, a checkout that degrades under load, product 404s, and a search
    latency tail
- **ticketing** - `server/examples/ticketing-api` (`:9100`)
  - Journey: `events → detail → seats → hold → pay`
  - Planted bugs: seat-contention 409s, a payment gateway that buckles in the on-sale rush, and
    sold-out 404s

Each ships a sample API server, a behavior graph + templates, and an importable **OpenAPI / HAR**
([`examples/imports/`](examples/imports)). Full reference - the **User manual**
([English](docs/guide.en.md) · [한국어](docs/guide.ko.md)); a hands-on 0→100 walkthrough:
[`examples/USAGE.md`](examples/USAGE.md).

---

## Roadmap

These are designed (and in part built and tested) but **not yet wired into the run path**. The
rest of this README and the [user manual](docs/guide.en.md) describe only what runs today; these
move into the body when they do:

- **Payload mutation** - the mutation engine (`null` / `empty-string` / `huge-number` /
  `negative` / `type-swap` against one JSON field at a time, `server/internal/load/mutate.go`)
  exists with tests, but no run path calls it yet. The `mutation` finding category is already
  reserved for it and does not fire until then.
- **Step reordering** - deviation today *abandons* journeys and *explores* unlikely transitions;
  visiting permitted steps out of their scripted order is not implemented yet.
- **Load-concentration profiles** - the time-shaped concurrency strategies aimed at a single
  target API (`constant` / `ramp` / `spike` / `soak` in `server/internal/load/strategy.go`) are
  built and tested but unwired. Today you concentrate load with a single-endpoint run or an
  open-model `spike` arrival shape.

---

## Architecture

A single Go binary (engine + load workers, with an embedded React control-plane UI). Local-first;
scales out to gRPC master/worker for large runs. Client-side observation is the core; server-side
metrics are opt-in.

```
server/                  Go backend module
server/cmd/tmula         entrypoint: serve, run, reproduce, init, bench, demo
server/internal/domain   core model: experiments, scenario graphs, virtual users, ...
server/internal/engine   scenario graph execution (dependency edges inviolable)
server/internal/load     virtual users, load profiles, protocol adapters
server/internal/workload open-model (arrival-rate) scheduler + capacity planning
server/internal/obs      observation collector, finding classification, mergeable summary
server/internal/safety   allowlist, rate cap, kill switch
server/internal/store    in-memory (local) + Postgres (distributed) persistence
server/internal/cluster  gRPC master/worker for distributed runs
server/internal/web      embedded React UI
server/internal/demo     the `tmula demo` shop SUT (planted bugs) + its embedded access log
server/proto             protobuf contracts for distributed workers
server/examples          Go sample API servers used by the demos
web/                     React + Vite control-plane UI
examples/                scenario files, imports, one-command demo, USAGE guide
```

---

## Requirements

- macOS / Linux for the prebuilt binary, **or** Go 1.25+ and Node 20+ to build from source
- `jq` + `curl` only for the manual demo script (`examples/run-demo.sh`); `tmula demo` needs
  nothing extra
- Docker + Postgres - optional, only for the distributed-store integration test

## License

Apache-2.0 — see [LICENSE](LICENSE).

---

<p align="center">
  Built by <a href="https://github.com/chordpli">chordpli</a>
</p>
