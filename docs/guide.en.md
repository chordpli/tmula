# tmula User Manual

Also available in: [한국어](guide.ko.md)

**tmula** is a real-user traffic simulator. Instead of firing identical requests at one endpoint, it drives *virtual users* through an explicit **behavior graph**. They move like real people: they branch, hesitate, occasionally go off-script, and pile onto whatever endpoint is hot. It then classifies what broke into **findings**. This guide is the companion to the [README](../README.md). It starts with onboarding, then becomes a reference, with care for every JSON field (the part most newcomers find intimidating).

You do not need to read this top to bottom. If you only want to *run something*, read [Get it running](#get-it-running) and [The web console, field by field](#the-web-console-field-by-field). When you hit the JSON editors and freeze, jump to [JSON formats - the complete reference](#json-formats--the-complete-reference). Everything here is grounded in the source: field names, defaults, and validation messages are quoted as they actually are.

---

## Table of contents

1. [Who this guide is for / mental model](#who-this-guide-is-for--mental-model)
2. [Get it running](#get-it-running)
3. [The web console, field by field](#the-web-console-field-by-field)
4. [JSON formats - the complete reference](#json-formats--the-complete-reference)
   - [Scenario graph](#scenario-graph)
   - [API templates](#api-templates)
   - [Personas / segments](#personas--segments)
   - [Workload](#workload-json)
   - [The full RunSpec](#the-full-runspec)
   - [Compressed scenario YAML](#compressed-scenario-yaml)
5. [The CLI](#the-cli)
6. [Findings explained](#findings-explained)
7. [Reading the results](#reading-the-results)
8. [Importing OpenAPI / HAR](#importing-openapi--har)
9. [Authenticated runs](#authenticated-runs)
   - [Which auth strategy? (start here)](#which-auth-strategy-start-here)
   - Strategies: [pool](#strategy-pool-pre-supplied-credentials) · [login](#strategy-login-mint-a-token-at-run-time) · [bootstrap-signup](#strategy-bootstrap-signup-provision-real-accounts) · [mint](#strategy-mint-self-issue-a-jwt-locally) · [exec](#strategy-exec-bring-your-own-token--escape-hatch)
   - [Session-cookie auth](#session-cookie-auth) · [Why secrets stay in-process](#why-secrets-stay-in-process)
   - [Web OAuth2 guide · basic/apiKey import · what's deferred](#web-oauth2-guide--basicapikey-import--whats-deferred) · [What happens with a refresh token](#what-happens-with-a-refresh-token)
10. [Distributed mode](#distributed-mode)
11. [Safety](#safety)
12. [The example domains](#the-example-domains)
13. [Troubleshooting & FAQ](#troubleshooting--faq)

---

## Who this guide is for / mental model

This guide is for anyone who wants to find the bugs real users would hit, without recruiting real users. You might be a developer wiring tmula into CI, or a PM/QA who never touches a terminal and lives in the web console. Both paths are covered. No prior load-testing background is assumed; terms of art are defined on first use.

### The behavior graph

The single most important concept is the **behavior graph** (also called the *scenario graph*). It is a directed graph that describes the journey users take through your API:

- **Nodes** are states. Most nodes are bound to an **API template** (one HTTP call) by an `apiTemplateId`. Reaching such a node means "make this request."
- **Terminal nodes** have *no* `apiTemplateId`. By convention these are named `done` and `exit`. Reaching one means the user *finished the journey* (`done`) or *left it* (`exit`) rather than making another call. They fire no request.
- **Edges** are transitions between nodes. Each edge has a **weight**, the relative likelihood a user takes it. From a node, the engine picks an outgoing edge in proportion to weights.
- **Dependency edges** (`dependency: true`) are hard preconditions: the target *requires* this predecessor and a user's random deviation may **never** skip it. (In the shop demo, `cart → checkout` is a dependency: you cannot check out without a cart.)

Here is the shop demo graph as a text diagram (this is exactly the `examples/shop` graph and the web "Branching shop" preset):

```
                 0.4
        ┌──────────────────► search ──0.65──► product ──0.45──► cart ══0.6══► checkout ──1.0──► done
        │                      │ 0.15           │ 0.25          ║ (dependency)
 browse ┤ 0.4                  ▼                ▼               ║
        ├──────────────────► category ─0.7──► product          └──0.4──► exit
        │  0.2                 │ 0.15
        └──────────► exit ◄────┘ 0.15  (every stage also drops a share into `exit`)

   ══►  dependency edge: never skipped       ──►  weighted transition
   done / exit are terminal nodes (no request) - done = completed, exit = left
```

A user starts at the **start node** (`browse`), walks the graph edge by edge up to **max steps** transitions, and stops when it reaches a terminal node or runs out of steps.

### Virtual users

A **virtual user** is one simulated visitor walking the graph. How virtual users are *generated over time* is the **workload model**, and there are two:

- **Closed model**: a fixed pool of `N` concurrent users that loop. The concurrency is exactly what you set. Think "50 users, always 50 in flight."
- **Open model**: user *sessions* arrive at a **rate** over time (e.g. 200 new users/second). The concurrent user count *emerges* from rate × session duration (Little's Law), the way organic public traffic behaves. This is the default for a public-facing service.

Between steps a user pauses for a **think time** (a random delay, because real people read and decide between actions). In the open model you can also split arrivals into **personas** (segments): weighted user types, each with its own entry node and pacing. You simulate "70% slow first-time browsers, 30% fast power users" instead of one homogeneous robot.

### The three modes

tmula surfaces issues in three overlapping ways:

- **Scenario-following**: does the happy path hold up under realistic, branching traffic?
- **Deviation**: a per-step probability (`deviationRate`, `0..1`; the console's **Deviation rate** field) that a user departs from the weighted happy path - abandoning the journey early or exploring an unlikely transition - without ever violating a dependency edge.
- **Load-concentration**: aim a whole run at one endpoint (a single-endpoint `tmula run --get /path`, or a one-node graph) and watch where it degrades or saturates.

> **Not in a run yet:** payload mutation, step reordering, and time-shaped concentration profiles are built but not wired into the execution path - see the [README roadmap](../README.md#roadmap). In particular the `mutation` finding category is reserved for payload mutation and does not fire today.

### What "findings" are

A **finding** is a classified problem tmula detected from the *client side* (status codes, latency, error patterns), with no server instrumentation required. There are four categories: `contract`, `availability`, `threshold`, and `mutation`, each with a severity. A run's output is "here is what real traffic would have broken." Findings also carry an **evidence bundle** - representative failing sessions with replay coordinates, plus status-code and timing distributions - and `tmula reproduce` can replay one of those sessions in isolation to tell a functional bug from a load-dependent one. See [Findings explained](#findings-explained) for how each is computed.

### Why this differs from a plain load tool

A plain load tool hammers one URL with identical requests and tells you a throughput number. It cannot tell you that *checkout 500s only when a cart exists and concurrency is high*, because it never models the journey or the dependency. tmula walks the real funnel, honors preconditions, deviates within the rules, and classifies the failures by *kind*. You get bugs, not just a number.

---

## Get it running

This section is intentionally brief; the [README Quickstart](../README.md#quickstart) is the canonical source. Pick whichever fits you.

**One command, zero setup: `tmula demo`.** With the binary installed (install script below), this runs the whole loop self-contained: it boots a tiny planted-bug shop on an ephemeral local port, *learns* a behavior graph from the shop's access log, replays the learned traffic against it (engine + web console on `--addr`, default `:8080`), and prints the findings with concrete next steps. See [`tmula demo`](#tmula-demo--the-whole-loop-one-command) for the full reference.

```bash
tmula demo                 # ~1 minute of learned traffic, then findings + next steps
```

**Docker (no toolchain).** One command brings up the console (real UI baked in) plus both example APIs:

```bash
git clone https://github.com/chordpli/tmula.git && cd tmula
docker compose up
```

Open <http://localhost:8080>. Inside the Compose network the engine reaches the example APIs by **service name**, so use `http://sample-api:9000` / `http://ticketing-api:9100` - not `localhost`. See the [FAQ](#troubleshooting--faq) for why.

**Install script (prebuilt binary, UI baked in).**

```bash
curl -fsSL https://raw.githubusercontent.com/chordpli/tmula/main/install.sh | sh
tmula --role local --addr :8080      # open http://localhost:8080
```

**Build from source** (needs Go 1.25+ and Node 20+):

```bash
make demo    # UI + engine + both example APIs, all locally (presets target localhost:9000/:9100)
make web     # build the React UI, embed it, serve the console on :8080
make build   # Go binary only - fast, but the UI is a placeholder page
```

> **Important: placeholder UI vs real UI.** A plain `make build` / `go build` embeds only a *placeholder* page that says "run `make web`". That is expected: the CLI needs no UI build. To get the real browser console you must embed the UI with `make web` (or use the Docker image / prebuilt binary, which already embed it). If the console looks empty and tells you to run `make web`, that is why.

---

## The web console, field by field

The console (<http://localhost:8080> after `make web`) has three configuration cards: **Target**, **Load model**, and **Scenario**, then a **Run** button and a live view. The help text quoted below is the actual UI copy from `web/src/i18n.ts`. Every `?` Help tooltip is reproduced where relevant.

The fastest start: in the **Scenario** card click **Start from a template** and pick **Branching shop**, **Concert tickets**, **Health check**, or **API read flow**. That fills the graph, templates, start node, and max steps for you (the ticketing preset also switches the Base URL to `http://localhost:9100`). Then tweak and Run.

### Card: Target

> *"Where the simulated traffic goes, and the hosts it is allowed to reach. Add worker addresses to fan the load out across machines."*

| Field | What it is | Sensible values | Gotcha |
|-------|------------|-----------------|--------|
| **Base URL** | The service under test (your staging or local server). | `http://localhost:9000`, `http://sample-api:9000` (Docker) | Must include scheme + host (+ port). |
| **Allowlist** | Comma-separated hosts traffic may reach. A guardrail so a run can never escape your target. | `localhost`, or `sample-api` under Docker | **The console does *not* auto-add the Base URL host.** You must put the target host in *both* Base URL and Allowlist, or every request is blocked (see below). |
| **Workers** | Optional. Comma-separated gRPC worker addresses to distribute the load. Blank = run on this machine. | blank, or `10.0.0.5:8080,10.0.0.6:8080` | See [Distributed mode](#distributed-mode). |
| **Aggregate on workers** | Checkbox. Each worker summarizes its shard instead of streaming every request; scales to millions of users. | off for most runs | Trades per-endpoint / run-length finding fidelity for bounded memory. Only meaningful with Workers set. |

> **The allowlist gotcha (the #1 first-timer trap).** In the code, `buildRunSpec` (`web/src/api.ts`) builds the allowlist by *only* trimming and splitting the Allowlist field. It does **not** add the Base URL host for you. The safety guard then blocks any request whose host is not on that list. So if you set Base URL to `http://sample-api:9000` but leave the Allowlist empty (or pointing elsewhere), **every request fails** and the run reads as "all errors." Always put the target host in both fields. (The compact scenario *file* path and the CLI *do* default the allowlist to the target's host; the web console does not.)

### Card: Load model

> *"How users hit your service. **Open** mimics organic traffic - users arrive at a rate over time. **Closed** holds a fixed pool that loops."*

| Field | What it is | Sensible values | Gotcha |
|-------|------------|-----------------|--------|
| **Workload** | `Open` (arrival rate, realistic) or `Closed` (fixed looping pool). | Open for public-facing services | Open is in-process only; no distributed workers (see RunSpec validation). |
| **Arrival rate** | Open only. New users per second. | 50-500 to start | Combined with session length this *is* your concurrency (Little's Law). |
| **Duration** | Open only. How long users keep arriving (seconds). | 30-3600 | Must be > 0 for an open run. |
| **Max concurrency** | Open only. Back-pressure cap; `0` = uncapped. | Set it on one box! (see FAQ) | `0` (uncapped) lets a heavy open run saturate your own machine. Also: ≤ 200 enables per-request live animation. |
| **Think time** | Pause between a user's steps, in ms, as min-max. | `200`-`800` | Requires `0 ≤ min ≤ max`. Zero means no pause (robotic). |
| **Personas** | Open only, advanced. JSON array of weighted user types, each with its own entry node and pacing. Blank = one uniform population. | blank to start | See [Personas / segments](#personas--segments). |

### Card: Scenario

> *"The journey users take. Each run starts at the start node and walks the graph for up to the max steps; the JSON below defines the nodes, edges, and the API each node calls."*

| Field | What it is | Sensible values | Gotcha |
|-------|------------|-----------------|--------|
| **Start node** | The node id where every user begins. | `browse` (shop) | Must be an id that exists in the graph. |
| **Max steps** | The longest path (number of transitions) a user may take before stopping. | 10-12 for the shop | Too low and users never reach `checkout`. |
| **Deviation rate** | The % chance, at each step, that a user wanders off the weighted path - exploring another path or giving up mid-way. Dependency edges are never violated. | `0` to follow the scenario exactly; 5-15 to shake out off-script bugs | The console takes a percent (0-100) and maps it onto the RunSpec's `params.deviationRate` (`0..1`). |
| **Virtual users** | Closed: the pool size. Open: a nominal upper bound. | 50 closed; any positive number open | In the open run this is *nominal* - sessions come from the arrival rate. It still must be > 0 (the experiment validation rejects 0). |
| **Show live traffic** | Checkbox: visualize the run while it streams. | on for small runs | Per-request animation when small (≤ 200 users / max-concurrency), an aggregate flow map above that. |
| **Scenario graph** (JSON) | Nodes + weighted edges. *Advanced.* | use a preset | A dependency edge must complete before its target runs. See [Scenario graph](#scenario-graph). |
| **API templates** (JSON) | The request each node sends: method, path, optional payload. *Advanced.* | use a preset | See [API templates](#api-templates). |
| **Import** | Turn an OpenAPI spec, a HAR recording, or an access log into the graph + templates (logs come with an [import coverage report](#learning-a-graph-from-an-access-log)). | upload or paste | See [Importing OpenAPI / HAR](#importing-openapi--har). |

When you press **Run**, the console assembles a [RunSpec](#the-full-runspec) and POSTs it. Two things happen automatically: it sends `users: []` plus a `userCount` for closed runs (so a huge run is a tiny request body, and the server synthesizes `u0..uN-1`), and it sizes the safety `rateCap` to your configured load. You never write those by hand in the UI.

---

## JSON formats - the complete reference

If the JSON editors intimidate you, read this section once. Each format is small and regular. Every field below lists its **type**, **meaning**, and an **example**, followed by a full worked example and the validation rules (with the exact error messages the server returns).

> **Tip:** you rarely write all of this by hand. The web **presets** and **Import** fill the graph + templates for you; the [compact scenario YAML](#compressed-scenario-yaml) is the shortest authoring path for the CLI. The raw RunSpec is for scripting and advanced control.

### Scenario graph

The graph the console's "Scenario graph" editor holds, and the `graph` field of a RunSpec.

```json
{
  "id": "shop",
  "nodes": [
    { "id": "browse",   "apiTemplateId": "t_browse" },
    { "id": "checkout", "apiTemplateId": "t_checkout" },
    { "id": "done" }
  ],
  "edges": [
    { "from": "browse",   "to": "checkout", "weight": 0.6, "dependency": true },
    { "from": "checkout", "to": "done",     "weight": 1.0 }
  ]
}
```

**Top level**

| Field | Type | Meaning |
|-------|------|---------|
| `id` | string | A label for the graph (e.g. `"shop"`). |
| `nodes` | array of Node | The states. At least one is required. |
| `edges` | array of Edge | The transitions. May be empty (a single-node probe). |

**Node**

| Field | Type | Meaning |
|-------|------|---------|
| `id` | string | Unique node id. Required; must be non-empty and not duplicated. |
| `apiTemplateId` | string | The API template this node calls. **Omit it to make the node terminal** (`done` / `exit`). A terminal node fires no request and marks completion or drop-off. |

**Edge**

| Field | Type | Meaning |
|-------|------|---------|
| `from` | string | Source node id (must exist in `nodes`). |
| `to` | string | Destination node id (must exist in `nodes`). |
| `weight` | number | Relative likelihood of taking this edge. Must be `>= 0` and finite. From a node, the engine picks an outgoing edge in proportion to weights. |
| `dependency` | bool | When `true`, the edge is a hard precondition: deviation never skips it. Default `false`. |

**Weight semantics.** Within a node, outgoing weights are relative, and the engine normalizes them. In the shop graph `browse → search (0.4)`, `browse → category (0.4)`, `browse → exit (0.2)` means roughly 40% / 40% / 20%. (The compact-scenario path applies the stricter rule that a node's outgoing transition weights sum to ≤ 1; a hand-written RunSpec graph only requires each weight to be `>= 0` and finite.)

**Dependency edges** carry the precondition independently of weight. A dependency edge can even have `weight: 0`: the walker records the requirement from `dependency: true`, and skips weight-0 edges as ordinary transitions. A `weight: 0, dependency: true` edge enforces "you must have done X first" *without* adding a traversable shortcut.

**Validation rules** (from `domain.ScenarioGraph.Validate`):

- `scenario graph: at least one node is required`
- `scenario graph: node id must not be empty`
- `scenario graph: duplicate node id "<id>"`
- `scenario graph: edge "<from>"->"<to>" references unknown node`
- `scenario graph: edge "<from>"->"<to>" has invalid weight <v>`: fires for negative, `NaN`, or `+Inf` weights.

### API templates

A **map** keyed by template id, in the console's "API templates" editor and the RunSpec `templates` field. Each value is one callable endpoint.

```json
{
  "t_browse":   { "method": "GET",  "path": "/browse" },
  "t_cart":     { "method": "POST", "path": "/cart",     "payloadTemplate": "{\"productId\":\"p7\",\"qty\":1}" },
  "t_checkout": { "method": "POST", "path": "/checkout", "payloadTemplate": "{\"total\":42}",
                  "headers": { "Authorization": "Bearer {{.token}}" } }
}
```

| Field | Type | Meaning |
|-------|------|---------|
| `method` | string | HTTP method: `GET`, `POST`, `PUT`, … Required. |
| `path` | string | Request path appended to the Base URL. Required. **Must be a rooted path** - see path-safety below. |
| `payloadTemplate` | string | Optional request body, as a string. Supports variable interpolation (below). |
| `headers` | object (string→string) | Optional static request headers. Values support interpolation. |

> The full domain `APITemplate` also has `id` and `protocol` fields, but in the templates *map* the key is the id and the protocol defaults to `rest`. The compact map form above (`{ method, path, payloadTemplate?, headers? }`) is what the presets, importer, and UI all use.

**Variable interpolation.** Bodies and header values are templates rendered per request with Go's `text/template` syntax `{{.name}}`:

- `{{.var}}`: a value from a virtual user's own `vars` map (when you supply per-user objects in a RunSpec).
- `{{.token}}`: the credential secret assigned to this user/session (only present on an [authenticated run](#authenticated-runs)).
- `{{.subject}}`: the credential's non-sensitive subject (e.g. a username).
- `{{.username}}` / `{{.password}}`: **login-flow bodies only** — the credential row the current virtual user is logging in as (`subject` = username, `token` = password). See [Log in N distinct accounts](#log-in-n-distinct-accounts-rows-as-credentials).
- `{{.userIndex}}`: the virtual user's number, in the **auth templates** — login/signup flow bodies, mint `subject`/`claims`, exec `command`/`env`, and `usersPattern` — for predictable per-user identities like `user{{.userIndex}}`.

A common authenticated header is `"Authorization": "Bearer {{.token}}"`.

**Path-safety rules** (from `runspec.validateTemplatePath`). A `path` must:

- start with a single `/` (a rooted path), else `must be a rooted path starting with /`;
- not start with `//`, else `must not start with // (protocol-relative authority)`;
- contain no `://`, else `must not contain a scheme`;
- contain no `\r`, `\n`, or `\t`, else `must not contain control characters`.

These rules exist so a template path can never redirect a request off your target host. (A variable that *renders into* the path is additionally caught at request time by the allowlist guard.) Also: `api: template "<id>": method is required` if `method` is empty.

### Personas / segments

**Open-model only.** The console's "Personas" editor and the RunSpec `segments` field: a JSON array of weighted behavioral profiles drawn from the arrivals. Each override is optional. A segment that sets only `name` and `weight` behaves like the run default but is tallied under its own identity.

```json
[
  { "name": "browser", "weight": 0.7, "start": "browse" },
  { "name": "buyer",   "weight": 0.3, "start": "cart",
    "maxSteps": 4, "thinkTime": { "minMs": 100, "maxMs": 300 } }
]
```

| Field | Type | Meaning |
|-------|------|---------|
| `name` | string | Labels the persona and tags its sessions. **Required and unique** within a run. |
| `weight` | number | The segment's relative share of arrivals. Must be `> 0`. Weights need not sum to 1; each segment's probability is its weight over the total. |
| `start` | string | Optional. Overrides the run's start node for this persona. Empty = run default. Must be a node in the graph. |
| `maxSteps` | int | Optional. Overrides the step bound. `0` = run default. Must be `>= 0`. |
| `thinkTime` | object | Optional. `{ "minMs": int, "maxMs": int }` overriding the run's think time. Must satisfy `0 <= minMs <= maxMs`. |

**Validation rules** (`domain.ValidateSegments` + RunSpec checks):

- `segment <i>: name is required`
- `segment "<name>": duplicate name`
- `segment "<name>": weight must be > 0 (got <v>)`
- `segment "<name>": maxSteps must be >= 0 (got <n>)`
- `api: segments (personas) apply only to the open workload model`: segments on a closed run are rejected.
- `api: segment "<name>" start node "<id>" is not in the graph`

### Workload (JSON)

The `workload` field of a RunSpec selects how users are generated. Omit it (or use a closed model) for the default fixed-pool behavior.

**Open model**

```json
{
  "kind": "open",
  "arrival": {
    "shape": "ramp",
    "startRate": 50,
    "peakRate": 500,
    "rampSeconds": 60,
    "holdSeconds": 600
  },
  "durationSeconds": 700,
  "maxConcurrency": 2000,
  "thinkTime": { "minMs": 200, "maxMs": 800 }
}
```

| Field | Type | Meaning |
|-------|------|---------|
| `kind` | string | `"open"` or `"closed"`. |
| `arrival.shape` | string | `constant` \| `ramp` \| `spike` \| `soak`. How the arrival rate moves over time. |
| `arrival.startRate` | number | Arrivals/sec at t=0 (the ramp/spike base). |
| `arrival.peakRate` | number | Arrivals/sec at the peak. |
| `arrival.rampSeconds` | int | Seconds spent climbing toward the peak. |
| `arrival.holdSeconds` | int | Seconds held at the peak. |
| `durationSeconds` | int | How long to keep arriving. Must be `> 0`. |
| `maxConcurrency` | int | Back-pressure cap on in-flight requests. `0` = uncapped. Must be `>= 0`. |
| `thinkTime` | object | `{ "minMs": int, "maxMs": int }`, the pause between a user's steps. |

**Shapes.** `constant` holds one rate; `ramp` climbs from `startRate` toward `peakRate`; `spike` jumps to a peak; `soak` is a long steady run. For `ramp` and `spike`, `peakRate` **must be > 0** (they are defined by their peak: `constant`/`soak` fall back to `startRate`, ramp/spike do not).

**Closed model**

```json
{ "kind": "closed", "concurrency": 50, "thinkTime": { "minMs": 0, "maxMs": 0 } }
```

| Field | Type | Meaning |
|-------|------|---------|
| `kind` | string | `"closed"`. |
| `concurrency` | int | The fixed number of looping users. Must be `> 0`. |
| `thinkTime` | object | Same as above. |

**Validation rules** (`domain.WorkloadModel.Validate`):

- `workload: invalid kind "<k>"`
- `think time: require 0 <= minMs <= maxMs (got <a>..<b>)`
- closed: `workload: closed model needs concurrency > 0`
- open: `workload: invalid arrival shape "<s>"`; `workload: arrival rates must be finite`; `workload: arrival rates must be non-negative`; `workload: open model needs a positive arrival rate`; `workload: ramp arrival needs peakRate > 0` (and `spike`); `workload: open model needs durationSeconds > 0`; `workload: maxConcurrency must be >= 0`.

### The full RunSpec

The complete, self-contained run definition: the raw body of `POST /api/experiments`. You only need this for scripting or advanced control; the console and the scenario file both produce one for you. This is the *real* shape (from `runspec.RunSpec` and what `web/src/api.ts` posts):

```json
{
  "experiment": {
    "name": "ui-run",
    "targetEnvId": "env",
    "scenarioGraphId": "graph",
    "params": { "virtualUserCount": 50, "deviationRate": 0, "authStrategy": "pool" }
  },
  "targetEnv": {
    "id": "env",
    "baseUrl": "http://localhost:9000",
    "allowlist": ["localhost"],
    "rateCap": { "maxRps": 1000, "maxConcurrency": 200 },
    "envClass": "dev"
  },
  "graph": { "...": "see Scenario graph" },
  "templates": { "...": "see API templates" },
  "start": "browse",
  "maxSteps": 12,
  "users": [],
  "userCount": 50,
  "seed": 1,
  "workload": { "...": "optional; see Workload" },
  "segments": [],
  "trace": true,
  "workers": [],
  "aggregateWorkers": false,
  "credentialPool": null
}
```

**Top-level fields**

| Field | Type | Meaning |
|-------|------|---------|
| `experiment` | object | Names the run and its run-time params. `name` required; `params.virtualUserCount` must be `> 0`; `params.deviationRate` in `[0,1]` (the per-step off-script probability - see [The three modes](#the-three-modes)); `params.authStrategy` is `pool` or `bootstrap-signup`. |
| `targetEnv` | object | The system under test + safety constraints. `baseUrl` required; `allowlist` non-empty; `rateCap.maxRps` and `rateCap.maxConcurrency` both `> 0`; `envClass` is `dev`, `staging`, or `prod-locked`. |
| `graph` | object | The [scenario graph](#scenario-graph). |
| `templates` | object | The [API templates](#api-templates) map. |
| `start` | string | The start node id. Required. |
| `maxSteps` | int | Max transitions per user. |
| `users` | array | Explicit virtual-user pool (closed). Usually empty - let the server synthesize. |
| `userCount` | int | Sizes the closed pool when `users` is empty: the server synthesizes `u0..u{userCount-1}` at run time, so a huge run is a small request body. An explicit `users` list wins; the open model ignores this. |
| `seed` | int64 | Random seed for reproducibility (the scenario file defaults this to `1`). |
| `workload` | object | Optional [workload model](#workload-json). Nil/closed = fixed pool (default). |
| `segments` | array | The [persona mix](#personas--segments) (open only). |
| `trace` | bool | Opt a small run into live per-request streaming for the traffic graph. Larger runs ignore it. |
| `workers` | array of string | gRPC worker addresses to distribute across. Empty = run locally. |
| `aggregateWorkers` | bool | Workers fold their shard into a compact summary instead of streaming every request. Ignored unless `workers` is set. |
| `credentialPool` | object | Optional [auth pool](#authenticated-runs). Nil = unauthenticated. |
| `metrics` | object | Optional. `{ "prometheusUrl": string, "queries": [{ "name", "query" }] }` - server metrics fetched into the report after the run. The host must be allowlisted. |
| `findings` | object | Optional. `{ "errorRate": number, "p95LatencyMs": number, "availabilityStreak": int }` - tunes the thresholds that classify observations into findings. An omitted (or `0`) field keeps its default. See [Tuning the thresholds](#tuning-the-thresholds-the-findings-block). |

> **`virtualUserCount` must be > 0 even for open runs.** This trips people up. `experiment.params.virtualUserCount` is validated `> 0` regardless of workload model (`experiment: virtualUserCount must be > 0`). In an open run it is a *nominal* field (the actual users come from the arrival rate), but you still must set a positive number. The scenario-file path sets it to `1` automatically for open runs.

**Key RunSpec-level validation rules** (`runspec.RunSpec.Validate`):

- `api: start node is required`
- `api: at least one virtual user is required`: every non-open path needs `len(users) > 0` *or* `userCount > 0`.
- `api: distributed workers are not supported with the open workload model`: the open model is in-process only.
- `api: a credential pool is not yet supported with distributed workers`.

### Compressed scenario YAML

The shortest authoring path. `tmula run scenario.yaml` reads this compact document and `Expand`s it into a full RunSpec, filling everything else with defaults. It is YAML *or* JSON (both parse, and field names match the json tags). Here is a complete example exercising every block:

```yaml
target: http://127.0.0.1:9000     # the system under test (required)
allow: [127.0.0.1]                # hosts the run may reach (defaults to the target's host)
users: 80                         # closed-model pool size (default 20; ignored when `open:` is set)
maxSteps: 10                      # default: the flow length
seed: 1                           # default 1
deviationRate: 0.1                # per-step chance of going off-script (default 0 = never)

flow:                             # the ordered journey (required, >= 1 step)
  - id: browse
    request: GET /browse          # "METHOD /path" shorthand
  - id: search
    request: GET /search
    weight: 0.7                   # probability of the edge to the next step (default 1)
  - id: cart
    request: POST /cart
    body: '{"productId":"p7","qty":1}'
    headers: { X-Client: tmula }
  - id: checkout
    request: POST /checkout
    body: '{"total":42}'
    dependsOn: cart               # marks the edge into checkout as a never-skipped dependency

# Switch to an organic (open) arrival-rate load instead of a fixed pool:
open:
  rate: 200                       # constant arrivals/sec  (or from/to + rampSeconds for a ramp)
  forSeconds: 30                  # required for an open run
  thinkMs: [200, 800]             # [min, max] pause between a user's steps
  maxConcurrency: 2000            # back-pressure cap (0 = uncapped)

# Optional persona mix (open model only):
segments:
  - { name: browser, weight: 0.7, start: browse }
  - { name: buyer,   weight: 0.3, start: cart }

# Optional auth - see "Authenticated runs":
auth:
  strategy: pool                  # pool (pre-supplied) | login (mint at run time) | bootstrap-signup (provision) | mint (self-sign) | exec (BYO command)
  users:
    - { subject: alice, token: jwt-aaa }
    - { subject: bob,   token: jwt-bbb }
  # or keep secrets out of the file:
  # source: { file: creds.csv, format: csv }   # csv | jsonl | tokens

# Optional finding thresholds - see "Findings explained":
findings:
  errorRate: 0.1                  # threshold finding above 10% overall errors (default 0.2)
  p95LatencyMs: 800               # threshold finding when p95 exceeds 800 ms (omit = gate off)
  availabilityStreak: 5           # consecutive failures on one API (default 5)
```

**Scenario fields**

| Field | Type | Meaning / default |
|-------|------|-------------------|
| `target` | string | Base URL. Required. |
| `allow` | array | Allowed hosts. **Default: the target's host** (the file path *does* derive it for you, unlike the web console). |
| `flow` | array of Step | The journey. Required, ≥ 1 step. |
| `users` | int | Closed pool size. Default `20`. Ignored when `open` is set. |
| `maxSteps` | int | Walk bound. Default: the flow length. |
| `seed` | int64 | Reproducibility. Default `1`. |
| `deviationRate` | number | Per-step probability (`0..1`) a user departs from the weighted path - abandons the journey or explores an unlikely transition; dependency edges are never violated. Default `0` (every user follows the happy path). Out of range → `scenariofile: deviationRate <v> out of range [0,1]`. |
| `open` | object | Switches to the open model (below). |
| `segments` | array | Persona mix; requires `open` (else `scenariofile: segments require an open workload`). |
| `auth` | object | Credentials (below). |
| `metrics` | object | Optional. Fetch Prometheus series over the run's window into the report (see [Server metrics](#server-metrics-side-by-side-prometheus)). |
| `findings` | object | Optional finding thresholds (below). |

**Step fields**

| Field | Type | Meaning |
|-------|------|---------|
| `id` | string | Unique node/template id. Required. |
| `request` | string | `"METHOD /path"` shorthand. Empty makes a pure state node (no request). |
| `body` | string | Request payload template. |
| `headers` | object | Static request headers. |
| `dependsOn` | string | An earlier step's id this one requires; the edge into this step becomes a never-skipped dependency. |
| `weight` | number | Probability of the edge to the *next* step. Default `1`. |

**`open` fields** (maps onto the [workload](#workload-json) model)

| Field | Type | Meaning |
|-------|------|---------|
| `rate` | number | Constant arrivals/sec. |
| `from` / `to` | number | Ramp start / peak rate. |
| `rampSeconds` / `holdSeconds` | int | Ramp / hold durations. |
| `shape` | string | Override: `constant`\|`ramp`\|`spike`\|`soak`. Defaults to `ramp` if `from`/`to` given, else `constant`. |
| `forSeconds` | int | **Required.** How long to keep arriving (`scenariofile: open.forSeconds must be > 0`). |
| `thinkMs` | `[min, max]` | Think-time range (must be exactly two ints, else `open.thinkMs must be [min, max]`). |
| `maxConcurrency` | int | Back-pressure cap. |

**`auth` fields**: see [Authenticated runs](#authenticated-runs). `strategy` is `pool` (default), `login`, `bootstrap-signup`, `mint`, or `exec`. For `pool`: `users` is a list of `{ subject, token }`, or `source: { file, env, format }`, or `usersPattern: { subject, token, count }` (the three are mutually exclusive). For `login`: `login.flow`, `login.capture.token` (optional; empty = auto-detected), `login.capture.subject` (optional), `login.scope` (`per-user` | `shared`). For `bootstrap-signup`: `signup.flow`, `signup.capture.token`, `signup.teardown` (optional), and `keepAccounts` (bool). For `mint`: the [mint block](#strategy-mint-self-issue-a-jwt-locally). For `exec`: the [exec block](#strategy-exec-bring-your-own-token--escape-hatch) (opt-in gated).

**`findings` fields** (every field optional; a `0`/omitted value keeps its default)

| Field | Type | Meaning / default |
|-------|------|-------------------|
| `errorRate` | number | Overall error rate above this → a `threshold` finding. Default `0.2`. Must be in `[0,1]` (else `scenariofile: findings: errorRate <v> out of range [0,1]`). |
| `p95LatencyMs` | number | Overall p95 latency (ms) above this → a `threshold` finding. Default `0` = the p95 gate stays **disabled**. Must not be negative. |
| `availabilityStreak` | int | This many consecutive failures on one API → an `availability` finding. Default `5`. Must not be negative. |

The same block (identical field names) is the RunSpec's `findings` field; see [Tuning the thresholds](#tuning-the-thresholds-the-findings-block).

How `Expand` defaults things: it derives the graph and templates from `flow` (each request-bearing step becomes a template `t_<id>`), links consecutive steps with weighted edges, sets a `rateCap` of `{ maxRps: 10000, maxConcurrency: 1000 }`, `envClass: dev`, and `start` to the first step. The compact graph is validated with the stricter scenario rules (transition weights in `[0,1]`, per-node outgoing sum ≤ 1, dependency edges form a DAG).

### Graph-first scenario files

When the journey branches, a linear `flow` cannot carry it. A scenario file may
hold the **graph itself** instead of a flow (the two are mutually exclusive).
This is the form [access-log learning](#learning-a-graph-from-an-access-log)
emits, and you can author it by hand too:

```yaml
target: http://localhost:9000
start: browse                      # required: the node every session begins at
maxSteps: 12                       # default: the node count
graph:                             # a scenario graph (same shape as the JSON reference)
  id: shop
  nodes:
    - { id: browse,   apiTemplateId: t_browse }
    - { id: checkout, apiTemplateId: t_checkout }
    - { id: exit }
  edges:
    - { from: browse, to: checkout, weight: 0.6, dependency: true }
    - { from: browse, to: exit,     weight: 0.4 }
templates:                         # the API template map (key = id, protocol defaults to rest)
  t_browse:   { method: GET,  path: /browse }
  t_checkout: { method: POST, path: /checkout, payloadTemplate: '{"total":42}' }
```

With `graph`, `start` is required (and must name a graph node), every node's
template must exist in `templates`, and the remaining blocks - `open`,
`segments`, `auth`, `users`, `deviationRate`, `findings` - behave exactly as in
the flow form. The same strict scenario validation applies.

---

## The CLI

The `tmula` binary is one command with subcommands. With no recognized subcommand it starts the long-running engine (`serve`).

### `tmula demo` - the whole loop, one command

The first-run experience: boot a built-in buggy shop, learn its behavior graph from traffic, replay that traffic, read the findings - no scenario file, no second terminal.

```bash
tmula demo [--addr :8080] [--duration 60s] [--no-browser]
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--addr <addr>` | `:8080` | Listen address for the demo's engine + web console. A taken port fails fast with an error that points at this flag (e.g. `--addr :8081`). |
| `--duration <dur>` | `60s` | How long the learned traffic keeps arriving. Must be positive. |
| `--no-browser` | false | Do not open the web console in a browser. (Opening uses macOS `open` / Linux `xdg-open`; on other platforms the demo warns and the printed URL is the fallback.) |

What it does, stage by stage (the staged `[1/4]`..`[4/4]` output mirrors this):

1. **Boots the demo shop** - a tiny store with planted bugs (a flaky cart, a checkout that degrades under load, a rare broken product link) - on an *ephemeral* loopback port, so the only port you can ever collide on is the engine's `--addr`.
2. **Learns a behavior graph** from the shop's bundled access log with the same [access-log learner](#learning-a-graph-from-an-access-log) `tmula init` uses: endpoints, branch weights, drop-offs, think time, and an open-workload suggestion. The pacing is compressed into the demo window (enough sessions that the planted bugs reliably surface); the learned graph and its weights are untouched.
3. **Starts the local engine + embedded web console** on `--addr` and runs the learned traffic against the shop. The run spec expands through the same scenario path every `tmula run` uses - the allowlist defaults to the shop's host only, and the [safety guard](#safety) applies in full. The run is created with live tracing on, and the browser is opened to `/?run=<run-id>`, so a console with the embedded UI attaches straight to the run's live view - the traffic flow map and live metrics, no form to fill.
4. **Prints the results** when the window closes: the standard run summary with findings, then **Next steps** - a ready-to-paste `tmula reproduce` command for the first finding, the run's `report.html` URL, the console URL, and the `tmula init --from <your access log>` / `tmula run` pair that points the same loop at your own service.

After the summary the demo **stays up** (engine and shop both) so those commands keep working; **Ctrl-C** shuts both down gracefully and exits `0` - at any stage, including mid-run. Failures exit `1`. There is deliberately **no findings gate**: the demo *finding* bugs is the success, so unlike `tmula run` there is no `--fail-on-findings` and findings never affect the exit code.

> **Embedded UI note.** The browser console page is only the real console in a binary that embeds the UI (the prebuilt/install-script binary, the Docker image, or a `make web` build). With a plain `go build` the demo still runs end to end - the terminal summary and the `report.html` URL are the result surfaces - but the console root shows the placeholder page. With the embedded UI, the `/?run=<run-id>` link the demo opens attaches the console straight to its run's live view (flow map + live metrics, then the report when the run finishes); without it, the demo's own run reports through the terminal summary and the linked HTML report.

### `tmula run` - run a scenario and print findings

Builds a RunSpec from a scenario file (or single-endpoint flags), executes it (in-process by default, or against a running `--engine`), and prints the findings.

| Flag | Default | Meaning |
|------|---------|---------|
| `--target <url>` | (from file) | Target base URL; overrides the scenario file's target. |
| `--get <path>` | - | Single-endpoint mode: GET this path (no scenario file). |
| `--post <path>` | - | Single-endpoint mode: POST this path. |
| `--users <n>` | 0 | Closed-model virtual user count. |
| `--open <rate>` | 0 | Open model: arrivals per second. |
| `--for <s>` | 0 | Open model: how long to keep arriving (seconds). |
| `--ramp-to <rate>` | 0 | Open model: ramp peak rate (uses `--open` as the start). |
| `--seed <n>` | 1 | Random seed. |
| `--engine <url>` | - | Run against an existing engine over HTTP instead of in-process. |
| `--auth-source <ref>` | - | Attach an external credential pool without editing the scenario: `file:./pool.csv` or `env:VAR`. Resolved in-process (the secret never crosses the wire); overrides any `auth` block the scenario declares. See [Authenticated runs](#authenticated-runs). |
| `--auth-format <fmt>` | (inferred) | Body format for `--auth-source`: `csv` \| `jsonl` \| `tokens`. Inferred from a `.csv`/`.jsonl` extension, else `tokens`. |
| `--keep-accounts` | false | Bootstrap-signup only: leave provisioned accounts in place (opt out of teardown). Without it, a signup pool with no teardown flow is rejected. |
| `--json` | false | Print the raw report JSON instead of a summary. |
| `--fail-on-findings` | false | Exit non-zero if any finding is detected (CI gate). |
| `--fail-on-severity <s>` | - | Gate only on findings at/above `warning` or `critical`. |
| `--baseline <run-id>` | - | Regression gate: diff this run's findings against a baseline run fetched from `--engine` (required with this form). Exit `3` only on findings **new** vs the baseline. See [the baseline gate](#the-baseline-gate--fail-only-on-regressions). |
| `--baseline-file <file>` | - | Regression gate against a saved report JSON (a previous `tmula run --json` output, e.g. a CI artifact). Use only one of `--baseline` / `--baseline-file`. |
| `--known-issues <file>` | - | Known-issues YAML: matching **new** findings are suppressed in the baseline gate until their `expires` date (`YYYY-MM-DD`). Requires `--baseline` or `--baseline-file`. |
| `--summary <file>` | `$GITHUB_STEP_SUMMARY` | Append a markdown run summary (stats + findings table) to this file. Defaults to GitHub Actions' step summary when that env var is set, so CI gets it with zero configuration. |
| `--timeout <dur>` | 2m | Max time to wait for the run to finish. |

**Worked examples:**

```bash
# A scenario file with a fixed pool of 50 users
tmula run examples/shop/scenario.yaml --users 50

# Single endpoint, no file - a quick "is it healthy under 20 users?" probe
tmula run --target http://localhost:9000 --get /health --users 20

# Organic open load: 278 arrivals/sec for one hour
tmula run examples/shop/scenario.yaml --open 278 --for 3600

# A ramp: start at 50/sec, climb to 500/sec, over the run
tmula run examples/shop/scenario.yaml --open 50 --ramp-to 500 --for 600

# CI gate: exit 2 if anything broke
tmula run examples/shop/scenario.yaml --users 50 --fail-on-findings

# CI gate, criticals only
tmula run examples/shop/scenario.yaml --users 50 --fail-on-severity critical

# Regression gate: exit 3 only on findings new vs main's saved report
tmula run examples/shop/scenario.yaml --users 50 \
  --baseline-file main-report.json --known-issues known-issues.yaml
```

**Exit codes** (precedence order - a later gate is consulted only when every earlier one passed):

| Code | Meaning | Trigger |
|------|---------|---------|
| `0` | Clean | The run completed and no enabled gate tripped. With a baseline, *persisting* findings still exit `0` - they are not what this change broke. |
| `1` | Error, or a failed/killed run | Always non-zero regardless of findings (e.g. a timeout or a kill-switch trip), so a broken run never silently passes CI. |
| `2` | Findings detected | `--fail-on-findings` / `--fail-on-severity`: the absolute gate. It counts every (matching) finding, baseline or not, so it keeps its meaning even when a baseline is also passed. |
| `3` | New findings vs the baseline | `--baseline` / `--baseline-file`: the regression gate, after known-issue suppression. |

The absolute gate counts every finding for `--fail-on-findings` or `--fail-on-severity warning`; `--fail-on-severity critical` counts only criticals.

The flag parser collects positionals in a loop, so `tmula run scenario.yaml --users 50` and `tmula run --users 50 scenario.yaml` both work.

### The baseline gate - fail only on regressions

`--fail-on-findings` is absolute: any finding fails the job, even one that has been failing for
weeks. The **baseline gate** compares instead. Findings are keyed by their stable identity
`(category, evidenceRef)` - no run-specific numbers - and diffed against a baseline run, so CI
goes red only for what *this* change broke. Two ways to name the baseline:

- `--baseline-file report.json` - a saved `tmula run --json` output. The natural CI shape: the
  main-branch job uploads its report as an artifact, the PR job downloads it and gates against it.
- `--baseline <run-id> --engine <url>` - fetch the baseline from a long-running engine's run
  history (`GET /api/runs/{id}/report`). An in-process run starts with empty history, which is
  why this form requires `--engine`.

The verdict has four buckets: **new** (the only one that fails the gate, exit `3`), **resolved**,
**persisting** (already broken in the baseline - reported, never failing), and **suppressed**. It
prints after the report, and is appended to the markdown `--summary` as a **"Baseline gate"**
section (one table, new findings first, suppressed rows annotated with their reason and expiry).

**Known issues.** `--known-issues <file>` suppresses accepted *new* findings. The file is a YAML
list; **every field is required**, so no suppression is anonymous or eternal:

```yaml
- category: contract
  evidenceRef: checkout            # the finding's identity, exactly as reports print it
  reason: known cart-service hiccup, tracked in SHOP-123
  expires: 2026-07-31              # YYYY-MM-DD - the last (UTC) day this suppression holds
```

Matching is exact on `(category, evidenceRef)` - no globbing - and unknown YAML fields are
rejected, so a typo cannot silently disable a suppression. `expires` forces a re-triage date: the
expiry day itself still suppresses, and from the next (UTC) day the entry stops working - the
finding turns **new** again (and fails the gate), and the expired entry is called out on stderr
(it survives `--json` mode there) and in the summary, so it gets re-triaged or deleted rather
than rotting in the file.

### Running in CI

The exit codes above make `tmula run` a merge gate: run the journey against the
service the job just built, and the step fails when the journey breaks. The
repo ships a **GitHub Action** that installs the binary, runs the scenario,
appends the markdown summary to the workflow page, and (optionally) posts it as
a PR comment:

```yaml
jobs:
  journey:
    runs-on: ubuntu-latest
    permissions:
      pull-requests: write        # only needed for comment: true
    steps:
      - uses: actions/checkout@v4
      - run: docker compose up -d my-service   # start the SUT however you do
      - uses: chordpli/tmula@main
        with:
          scenario: tests/journey.yaml
          target: http://localhost:9000
          users: 50
          fail-on: critical        # findings | warning | critical | none
          comment: true            # post the summary on the PR
```

**Gating on regressions from the action.** The action's inputs mirror the
CLI's baseline gate:

- `baseline-file`: path to a previous `tmula run --json` report (the natural
  shape: the main-branch job uploads its report as an artifact, the PR job
  downloads it). Setting it turns on the regression gate - the job fails with
  exit `3` only on findings **new** vs the baseline. Combine with
  `fail-on: none` to gate purely on regressions.
- `known-issues`: path to a known-issues YAML, passed through to
  `--known-issues`. Only valid together with `baseline-file` (the CLI rejects
  it alone, early, before the run).
- `pr-comment: 'true'`: on `pull_request` events, posts the **Baseline gate**
  verdict table as a PR comment and *edits the same comment in place* on
  re-runs (unlike `comment`, which adds a new comment every run). Without a
  baseline it posts the full run summary instead. It needs the workflow's
  `pull-requests: write` permission; fork PRs get a read-only token, so there
  the comment is skipped with a warning - the job's red/green always tracks
  the run's own exit code, never the comment plumbing.

```yaml
      - uses: actions/download-artifact@v4
        with: { name: tmula-baseline, path: baseline }   # main's saved report
      - uses: chordpli/tmula@main
        with:
          scenario: tests/journey.yaml
          target: http://localhost:9000
          fail-on: none                        # gate on regressions only
          baseline-file: baseline/report.json  # exit 3 only on NEW findings
          known-issues: tests/known-issues.yaml
          pr-comment: true                     # upsert the gate verdict on the PR
```

Without the action, plain `tmula run` already cooperates with GitHub Actions:
when `GITHUB_STEP_SUMMARY` is set the markdown summary is appended there
automatically, and the summary is written even when the run failed or the gate
trips - a red job links straight to *what broke*, not just an exit code. To
gate on regressions only, add `--baseline-file` (exit `3` on new findings - see
[the baseline gate](#the-baseline-gate--fail-only-on-regressions)); its verdict
table is appended to the same step summary.

### `tmula reproduce` - real bug, or load artifact?

Every per-endpoint finding's [evidence bundle](#the-evidence-bundle) names sessions with their
**seed coordinates** (run seed + user index = session seed). `tmula reproduce` replays the
finding's first evidence session **in isolation** - one session, no concurrent load, the same
deterministic walk - several times, and classifies the root cause from how often the failure
recurs:

| Verdict (`rootCauseClass`) | Meaning |
|----------------------------|---------|
| `functional` | Reproduced on **every** attempt: it does not need load - likely a plain functional bug. |
| `load-dependent` | Reproduced on **no** attempt: it likely needs the original concurrency or saturation. |
| `flaky` | Reproduced on some attempts only - rerun with more `--attempts`, or inspect the target. |

| Flag | Default | Meaning |
|------|---------|---------|
| `--engine <url>` | - | **Required.** The engine that ran (and still holds) the run. |
| `--run <run-id>` | - | **Required.** The run whose finding to replay. |
| `--finding <sel>` | - | **Required.** `category/evidenceRef` (e.g. `contract/checkout` - the same key reports print) or the finding's 1-based index in the run's findings list. |
| `--attempts <n>` | 3 | How many isolated replays to run (1-20). |
| `--json` | false | Print the raw reproduce JSON instead of the table. |
| `--timeout <dur>` | 2m | Max time to wait for the replays. |

```bash
tmula reproduce --engine http://localhost:8080 --run run-12 --finding contract/checkout
tmula reproduce --engine http://localhost:8080 --run run-12 --finding 1 --attempts 5
```

The output shows the session and its seed arithmetic, the original failure path, every attempt's
per-step status codes (the step carrying the finding's signal marked with `!`), and the verdict.
The verdict is also stamped onto the stored finding as `rootCauseClass` - live cache and Store
both - so a refetched report shows the triage already done (`(root cause: load-dependent)`).

The replay honors the original run's constraints: the safety guard is rebuilt and enforced
(allowlist, rate cap, a `prod-locked` target is refused), a persona session replays with its
segment's entry-node and max-steps overrides, and a credential-pool run re-acquires the same
credential by index.

**Limits.** The replay happens on the engine (`POST /api/runs/{id}/reproduce`), because the seed
coordinates only mean something next to the run's spec - which lives in engine memory only. A
restarted engine or an evicted run answers `410`; run against a long-running engine when you
intend to reproduce later. Summary-derived (run-wide) findings carry no session coordinates, and
`mutation` findings need inputs the replay does not generate - both are refused (`422`). And the
verdict is **a signal, not a proof**: the replay recreates the session's *traffic composition*
(same seed, same walk), never the original timing, concurrency, or target state - the engine
repeats this note with every result.

### `tmula bench` - capacity probe

Drives the bench harness at a target concurrency against a SUT and prints capacity metrics (achieved RPS, latency percentiles, tracking error). It uses the bench harness directly, not the control plane.

| Flag | Default | Meaning |
|------|---------|---------|
| `--target` / `--get` / `--post` | - | Same scenario / single-endpoint forms as `run`. |
| `--users <n>` | 50 | Target concurrency. |
| `--max-steps <n>` | flow length | Max transitions per user. |
| `--timeout <dur>` | 10s | Per-request transport timeout. |
| `--seed <n>` | 1 | Seed. |
| `--json` | false | Raw result JSON. |

```bash
tmula bench examples/shop/scenario.yaml --users 100
tmula bench --target http://localhost:9000 --get /health --users 50
```

### `tmula init` - scaffold a scenario from a spec or traffic

Turns an existing API description (OpenAPI or HAR) or an **access log** into a scenario file, so you start from your real endpoints - or your real traffic - not a blank page.

| Flag | Default | Meaning |
|------|---------|---------|
| `--from <file>` | - | **Required.** OpenAPI, HAR, or access-log file. |
| `--format <f>` | auto | `auto` \| `openapi` \| `har` \| `accesslog`. |
| `--out <file>` | stdout | Where to write the scenario. |
| `--target <url>` | - | Override the target base URL (required for logs, which carry no host). |

```bash
tmula init --from examples/imports/shop.openapi.yaml --out scenario.yaml
tmula init --from access.log --target http://staging:9000 --out scenario.yaml
tmula run scenario.yaml --users 50
```

OpenAPI/HAR scaffold a linear flow; an access log goes further - tmula [*learns* the graph from the traffic](#learning-a-graph-from-an-access-log) and emits a graph-first scenario with the observed branch weights filled in.

### `serve` (default) and roles

Any invocation without `run` / `reproduce` / `init` / `bench` / `demo` starts the long-running engine.

| Flag | Default | Meaning |
|------|---------|---------|
| `--role <r>` | local | `local` \| `master` \| `worker`. |
| `--addr <addr>` | :8080 | HTTP listen address (control plane + UI); for a worker, the gRPC listen address. |
| `--workers <csv>` | - | Comma-separated worker addresses; experiments without their own workers distribute across these. |
| `--store <file>` | - | local role: JSON snapshot file, loaded on start and written on graceful shutdown, so run history survives a restart. |
| `--db-dsn <dsn>` | - | master role: Postgres DSN for the durable store (falls back to in-memory when blank; env `TMULA_DB_DSN` is used if unset). |
| `--version` | - | Print version and exit. |

```bash
tmula --role local  --addr :8080                                  # engine + API + embedded UI
tmula --role worker --addr :9101                                  # a distributed worker (gRPC)
tmula --role local  --addr :8080 --workers 127.0.0.1:9101,127.0.0.1:9102   # a master with a worker pool
```

Health check: `GET /healthz` returns `{"status":"ok","role":...,"version":...}`.

---

## Findings explained

A finding is `{ runId, category, severity, evidenceRef, firstSeen, description, count }` (`count` is the number of occurrences behind it; omitted for threshold findings, which carry rates in the description). It optionally carries an `evidence` bundle ([below](#the-evidence-bundle)) and - after a `tmula reproduce` pass - a `rootCauseClass`. `evidenceRef` is the finding's *stable identity* with no run-specific numbers in it - the API (node) id for per-endpoint findings, `error-rate` / `p95-latency` for the run-wide threshold findings, `run-wide` for summary-derived aggregates - so the same issue keys identically across runs; it is what the baseline gate diffs on and what a known-issues entry names. There are four categories and three severities (`critical`, `warning`, `info`). The single-node path classifies per API per category, so *one* bad endpoint yields *one* finding, not hundreds, in the order mutation → contract → availability → threshold. Here is how each is computed (`server/internal/obs/finding.go`). First, two predicates used throughout:

- A request **failed** when `statusCode >= 400` **or** it carries an `errorClass` (e.g. `"timeout"`, `"transport"`, `"assertion"`).
- A request is **unavailable** when `statusCode >= 500` **or** `errorClass == "timeout"` **or** `errorClass == "transport"`.

| Category | Severity | Exactly how it's computed |
|----------|----------|---------------------------|
| **contract** | critical | A **non-mutated** request that returned a **5xx** or failed an **assertion** (`contractSignal`). This is "the happy path produced an error a developer likely missed." One finding per API: *"N contract violation(s) on `<api>` (unexpected error on the happy path)."* |
| **mutation** | warning | A **mutated** input that **failed** (`mutationSignal`). Mutation testing fails inputs on purpose, so this is informational. One finding per API: *"mutated input surfaced N error(s) on `<api>`."* **Reserved:** payload mutation is not yet wired into the run path ([roadmap](../README.md#roadmap)), so no request is marked mutated and this category does not fire today. |
| **availability** | critical | An API that suffered **`AvailabilityRun` or more consecutive `unavailable()` results**. The streak is evaluated per endpoint in timestamp order (so it is robust to the order results stream back). Disabled when `AvailabilityRun <= 0`. One finding per API: *"N consecutive failures on `<api>` (saturation or downtime)."* |
| **threshold** | warning | Run-wide, excluding mutated requests. Two possible findings: error rate (`failed / total`) **>** `ErrorRateThreshold` → *"error rate X exceeded threshold Y"*; and p95 latency **>** `P95LatencyMs` (when that gate is `> 0`) → *"p95 latency Xms exceeded threshold Yms."* |

### Tuning the thresholds (the `findings` block)

The thresholds are configurable per run: both the RunSpec and the compact scenario file take an optional `findings` block (`obs.FindingConfig`):

```yaml
findings:
  errorRate: 0.1          # default 0.2
  p95LatencyMs: 800       # default 0 (gate disabled)
  availabilityStreak: 3   # default 5
```

| Field | Meaning / default |
|-------|-------------------|
| `errorRate` | Overall error rate above this → a threshold finding. Default `0.2`. |
| `p95LatencyMs` | Overall p95 latency above this many ms → a threshold finding. Default `0`, which keeps the p95 gate **disabled**. |
| `availabilityStreak` | This many consecutive failures on one API → an availability finding. Default `5`. |

Every field is optional, and a `0` (or omitted) field falls back to its default - a spec without the block classifies exactly as before the block existed. Note the asymmetry this implies: the block can *tighten or loosen* the error-rate and availability gates but cannot disable them (`0` means "default", not "off"), while the p95 gate is off unless you set it. Internally the block resolves into `ClassifyConfig` (`ErrorRateThreshold` / `P95LatencyMs` / `AvailabilityRun`), where a resolved `0` disables that gate. The block applies on every path - single-node, distributed-streaming, and distributed-summary (classification happens on the master, so nothing moves to the workers).

> **The distributed/aggregated path.** When workers aggregate their shards (`aggregateWorkers`), findings come from a merged `Summary` (`FindingsFromSummary`), which keeps only per-category tallies: no per-API breakdown and no ordering. It produces at most *one* finding per tripped category for the whole run, deliberately coarser than the per-endpoint single-node findings. That is the fidelity-for-scale trade you opt into. (A detail: the Summary's threshold error-rate/p95 count *all* observations including mutated ones, whereas the single-node classifier excludes mutated requests. This is latent today because the distributed path never carries mutated observations.)

### The evidence bundle

A finding answers *what* broke; the **evidence bundle** attached to it answers *who hit it, when,
and how to see it again*. It is condensed once, at classification time, from the same
observations the classifier counted - so the evidence can never disagree with the finding it
backs - and it is bounded by design: a finding stays small no matter how many requests failed
behind it. On the wire (the report JSON and the Store) it is the finding's optional `evidence`
field:

| Field | What it is |
|-------|------------|
| `vus` | Up to **5 representative sessions**: the earliest occurrences (where the issue first surfaced) plus the slowest of the rest (the worst case). For the p95 threshold finding the candidates are the requests *over the gate* - slowness, not failure, is that signal. |
| `vus[].vu` | The session ID - the exact `X-Tmula-Session-ID` header value the session sent on every request, so you can grep the target's logs for that one journey. |
| `vus[].seed`, `vus[].userIndex` | The replay coordinates: run seed + `userIndex` = the session's walk seed (`seed`). The index is the pool index for the closed model, the arrival number for the open model, the global user index for a distributed shard. This is what `tmula reproduce` replays from. |
| `vus[].persona` | The segment the session was drawn from (omitted when the run had no persona mix). |
| `vus[].path` | The node sequence the session walked, up to and including the failing request. (The distributed stream carries no per-request path, so it is empty there.) |
| `vus[].statusCode` / `latencyMs` / `errorClass` / `ts` | The request that surfaced the issue. Status `0` means a transport-level failure - the `errorClass` is the signal then. |
| `timeBuckets` | The finding's occurrences over four fixed quarters of the observed run window (`0-25%` … `75-100%`) - enough to tell "early in ramp-up" from "late in soak". |
| `statusCounts` | The status-code tally of *every* occurrence, not just the representative sessions. |

The web console renders the bundle as a collapsible panel per finding, and the standalone HTML
report has the same section. Both survive shared (masked) reports: the wire names `vus`/`vu` are
deliberately chosen so the PII masker - which redacts any field whose *name* looks sensitive,
including "session" - carries these synthetic identifiers through intact.

Two producers attach no evidence: summary-derived findings (`aggregateWorkers` folds per-request
data away by design - see the note above) and findings persisted before the bundle existed. A
finding without evidence renders exactly as before, and `tmula reproduce` refuses it.

---

## Reading the results

When **Show live traffic** is on, the console streams a live view; afterward you get a report with links.

**Traffic-flow map.** Requests enter on the left and fan toward an outcome on the right (a funnel). Edge **thickness is request volume** (a logarithmic scale, so a 12-request edge and a 12-million-request edge stay legible together), and edge **color tints from green to red by error ratio**. Edges into terminal nodes are rendered as outcomes: inflow to `done` reads as **completed**, inflow to `exit` as **left**. These are journey outcomes, not requests, so they are excluded from the "N requests" headline. For small runs (≤ 200 users, or ≤ 200 max-concurrency in the open model) the view **animates each request as a dot** (green = ok, red = error); above that cap it falls back to the aggregate flow map (per-edge counts), which scales to any run size because the payload is bounded by the edge count, not the request count.

**Latency heatmap.** A 2-D histogram: rows are latency bands (low → high; the top band is unbounded, e.g. "5s+"), columns are time buckets since the run started, and each cell's color is the request count in that band × time (darker = denser). This is the "where did latency go" view. A thin hot tail at the top means a slow minority.

**Live metrics.** Running counters: requests, error rate, p50 / p95 / p99 / max latency, timeouts, and a status-code tally (e.g. `200:313 500:8`).

**HTML report & compare.** Every run has a standalone server-rendered **HTML report** (`View full HTML report`) and a run-to-run **compare** view (`Compare with previous run`) for spotting regressions. In both the web report and the HTML report, each finding expands into its [evidence panel](#the-evidence-bundle): the representative sessions (with the `X-Tmula-Session-ID` value to grep server logs for and the seed coordinates `tmula reproduce` replays from), the status-code distribution, the timing distribution, and the `rootCauseClass` verdict when a reproduce pass recorded one.

### Server metrics side-by-side (Prometheus)

Client-side observation only says *what broke*. Attach a `metrics:` block to a
run and, **after the run, over exactly its time window**, the named PromQL
queries are fetched from Prometheus and shown beside the client-side stats -
so you can read "the connection pool drained at the same moment checkout 5xxs
spiked" on one screen.

```yaml
metrics:
  prometheus: http://localhost:9090
  queries:
    - { name: db conns, query: pg_stat_activity_count }
    - { name: cpu,      query: rate(process_cpu_seconds_total[30s]) }
```

- **Where it shows.** The web report (a sparkline per series with
  min/last/max), the standalone HTML report (a table), and the `--summary`
  markdown (a table).
- **Opt-in and never fails the run.** A dead Prometheus or a bad query becomes
  a note on the report; series from the queries that succeeded still render.
- **Allowlist applies.** Like every other host the engine reaches, the
  Prometheus host must be on the allowlist.
- **Live reports only.** The series are not persisted, so a report rebuilt
  after a restart/eviction omits them.
- At most 5 series per query, sampled at ~60 steps across the run window, so
  an over-broad query cannot swamp the report.

In a raw RunSpec this is the `metrics: { "prometheusUrl": ..., "queries": [...] }` field.

**Share links.** You can mint a read-only **viewer** share link for a report. The viewer is told *"Read-only. Sensitive fields are redacted."* The run's `killReason` is scrubbed on a shared report, so an internal kill reason never leaks to an external viewer. Shared links can carry an expiry; an expired or unknown token yields a localized "expired"/"not found" message.

---

## Importing OpenAPI / HAR

You rarely need to hand-author the graph + templates. The importer turns an **OpenAPI** spec, a **HAR** recording, or an **access log** into a ready-to-edit scenario (graph + templates + start + maxSteps).

- **Web console:** Scenario card → **Import from OpenAPI / HAR** → upload a file or paste the text → **Import**. The graph and templates fields fill in; review and Run. (The endpoint is `POST /api/import?format=auto|openapi|har|accesslog`.)
- **CLI:** `tmula init --from <openapi.yaml|session.har|access.log> --out scenario.yaml`.

**Format detection** is structural, not just by extension (`detectFormat`): a `.har` name forces HAR and a `.log` / `.jsonl` name forces access log; otherwise the document is parsed and its keys inspected. OpenAPI markers (`openapi` / `swagger` / `paths`) are checked first, then a HAR's `log.entries`, and finally a first line that parses as a log record means an access log. This makes a real browser HAR import correctly even without its `.har` name, and a log without its `.log` name.

**Journey-ordering heuristic.** The OpenAPI importer orders the imported steps into a plausible user journey (e.g. list before detail, reads before writes) rather than spec order, so the resulting flow reads like a real path through the API. Review the generated steps, fill in path parameters and request bodies, then run.

Ready-made examples live in [`examples/imports/`](../examples/imports): `shop.openapi.yaml`, `shop.openapi`'s HAR sibling `shop-session.har`, the traffic sample `shop-access.log`, and `ticketing.openapi.yaml`. They target `http://localhost:9000`, lining up with the bundled `server/examples/sample-api`.

### Learning a graph from an access log

An OpenAPI spec only knows *which* endpoints exist, and a HAR knows *one* path
through them. An **access log** records how all your real users actually moved,
so tmula **learns** the behavior graph itself from it. The result is a
miniature of the observed traffic: replay it against staging and you see where
the real traffic pattern breaks things, before a deploy.

- **Input - six formats, auto-detected from content.** Apache/nginx **combined
  format**; generic **JSON lines** (one object per line; common key spellings -
  `time`/`ts`/`timestamp`, `method`+`path` or `request`, `remote_addr`,
  `user_agent`); **AWS ALB** access logs; **CloudFront standard logs** (the
  tab-separated W3C format - the parser follows whatever column order the
  `#Fields` header declares); **Caddy** structured JSON; and **Traefik** JSON
  access logs. Detection (`importer.DetectAccessLogFormat`) probes the first
  few non-empty lines, so a file whose first line was truncated by log
  rotation still detects.
- **Sessionization.** Requests group per client (IP + user agent) and split
  into visits on a 30-minute idle gap.
- **Endpoint collapsing.** Queries are stripped and volatile segments (numbers,
  UUIDs, long hex ids) fold into `{id}`, so `/product/123` and `/product/456`
  become one node. Only the 30 hottest endpoints stay; transitions across a
  folded endpoint bridge over it (and the import reports how many were folded -
  nothing is silently dropped).
- **What is learned.** Transition frequencies → edge weights, session ends →
  `exit` edges, the most common first request → the start node, inter-request
  gap quartiles → think time, the session arrival rate → an `open` suggestion,
  the p95 session length → maxSteps.
- **Runnable first.** Logs carry no bodies, so each template's path is the
  *most observed concrete path* for that endpoint (e.g. `/product?id=1023`).
  The generated scenario runs as-is; fill in bodies/headers later.

The learner emits a [graph-first scenario file](#graph-first-scenario-files)
rather than a linear flow - the branching *is* the learned information. Paste a
log into the web console's Import and the graph + template editors fill in
directly.

**The coverage report - what the learner kept and dropped.** A capped or noisy
import must not silently pass as full coverage, so the learner reports its
stats: requests used, lines skipped, sessions, distinct clients, and folded
endpoints, plus up to ten *sampled parse failures* (line number, the line's
first 120 characters, and the reason) so a half-broken real-world log explains
*why* coverage is partial. Lines filtered by design (static assets, non-journey
methods) are counted but never sampled. The surfaces:

- `tmula init` prints the summary note on stderr (`learned N endpoint(s) from
  N request(s) across N session(s)…; skipped N unusable line(s)`).
- `POST /api/import` can return the stats as an optional `stats` object
  (omitted when the wired importer does not produce them, so old clients see
  the pre-stats response unchanged).
- The web console renders the stats as an **Import coverage** panel above the
  graph preview: the used/skipped/sessions/clients/folded summary, a warning
  tone when any line was skipped ("this import reflects only part of the
  captured traffic"), and a skipped-line samples table when the server sent
  samples.
- A log with **zero usable requests** fails the import, and the error message
  carries the first sampled failure's line number and reason - actionable from
  the error text alone.

**Variable promotion (Go API).** By default each collapsed `{id}` endpoint pins
to its single most-observed concrete path. The Go API can instead *promote*
those segments into template variables: `importer.FromAccessLogWithOptions`
with `PromoteVariables` emits paths like `/product/{{.product_id}}` (the
standard `{{.var}}` [template interpolation](#api-templates)) and reports each
variable's observed value pool (hottest first, capped at `MaxVariableSamples`,
default 5) in the stats. Variables with the same name share one pool across
endpoints - `/product/{id}` and `/product/{id}/reviews` draw from the same
`product_id` values. It is **off by default** because a promoted scenario only
renders once the caller seeds those pools into the virtual users' `vars`; the
default concrete paths run as-is.

The learner's knobs - the format hint, the node cap (default 30; a negative
value removes the cap), the session gap (default 30 minutes), and variable
promotion - live on `importer.AccessLogOptions` /
`importer.FromAccessLogWithOptions` in the Go API. The CLI and the web import
use the defaults (auto-detection included).

---

## Authenticated runs

To make simulated traffic carry real auth material, attach a **credential pool**. Each closed virtual user (by **user index**) or open session (by **session/arrival index**) is assigned a credential, wrapping around when there are more users than entries. Reference it from a template header (`"Authorization": "Bearer {{.token}}"`), or use `{{.subject}}` for the non-sensitive principal.

There are five credential strategies. Pick the one that fits your service.

### Which auth strategy? (start here)

Key the choice on what you already **have**:

| You have… | Strategy | Realism | Setup cost | IdP load | Distributed workers | Web-console path |
|-----------|----------|---------|------------|----------|---------------------|------------------|
| **A pile of tokens** (pre-issued bearer tokens / API keys) | [pool](#strategy-pool-pre-supplied-credentials) | Medium — static tokens, no issuance traffic | Lowest — paste or point at a file | None | ✅ via an external `source` (file/env reference) | **I already have tokens** |
| **A login endpoint + accounts** | [login](#strategy-login-mint-a-token-at-run-time) | High — every token is really issued | Low — one flow step + credentials | Prewarm, parallel but bounded (≤ 16 concurrent) | ❌ in-process only | **Log in to get tokens** |
| **The JWT signing key** (self-issued tokens) | [mint](#strategy-mint-self-issue-a-jwt-locally) | High — real per-user JWTs, no login traffic | Medium — key reference + claims | None | ✅ ships only the key reference | **Sign a token locally** (Advanced) |
| **An OAuth2 IdP** | login, assembled by the [web OAuth2 guide](#web-oauth2-guide--basicapikey-import--whats-deferred) | High | Low — answer two questions | Prewarm, bounded (≤ 16) | ❌ in-process only | **It's an OAuth2 service** |
| **Only a CLI that prints tokens** | [exec](#strategy-exec-bring-your-own-token--escape-hatch) | Depends on the command | Medium — `--allow-exec` opt-in | None (unless the command calls one) | ❌ in-process only | **Run a command for the token** (Advanced) |

And by **goal**:

- **Auth0 client-credentials (machine token)** → the web [OAuth2 guided mode](#web-oauth2-guide--basicapikey-import--whats-deferred), answering "With a client key" — or the [client_credentials worked example](#client_credentials-shared-machine-token).
- **Log in a 50k-user CSV** → [login + `source` rows](#log-in-n-distinct-accounts-rows-as-credentials) (one row = one account).
- **The login sets a session cookie, not a body token** → [Session-cookie auth](#session-cookie-auth).
- **Per-user JWTs without an IdP** → [mint](#strategy-mint-self-issue-a-jwt-locally) (you must hold the signing key).
- **I only have an issuer URL** → the OAuth2 guide's issuer discovery (**Fetch endpoints** fills the token URL from `/.well-known/openid-configuration`).

### Strategy: pool (pre-supplied credentials)

The simplest form — author tokens directly in the scenario file:

```yaml
auth:
  strategy: pool          # default when auth: is present; may be omitted
  users:
    - { subject: alice, token: jwt-aaa }
    - { subject: bob,   token: jwt-bbb }
```

To keep secrets out of the file entirely, use an **external source** instead of `users`:

```yaml
auth:
  strategy: pool
  source:
    file: creds.csv       # path relative to the scenario file; or use `env:` for a variable
    format: csv           # csv | jsonl | tokens
```

`source` and `users` are mutually exclusive. Supported formats:

| Format | Body shape | Subject |
|--------|-----------|---------|
| `csv` | header row `subject,token`, one credential per line | from `subject` column |
| `jsonl` | one `{"subject":"…","token":"…"}` object per line | from `subject` field |
| `tokens` | one raw secret per line | **empty** — `{{.subject}}` renders as `""` |

> **`tokens` format gotcha.** There is no subject column, so `{{.subject}}` in a header or body template renders as an empty string. Use `csv` or `jsonl` if your teardown or path templates need `{{.subject}}`.

An external source pool can fan out across **distributed workers** — each worker resolves its own slice locally; only a secret-free reference crosses the wire. Inline `users` pools and login/bootstrap strategies cannot fan out to workers (mint can — see [Strategy: mint](#strategy-mint-self-issue-a-jwt-locally)).

#### Generate accounts from a pattern (`usersPattern`)

To prepare tens or hundreds of thousands of accounts without a file, declare a subject/token template pair and a count. It materializes at Expand time; the secret template never crosses the wire (only the materialized secrets, still `json:"-"` masked).

```yaml
auth:
  strategy: pool            # or login (there subject=username, token=password)
  usersPattern:
    subject: "user{{.userIndex}}"
    token: "pw-{{.userIndex}}"
    count: 100000
```

`usersPattern` is mutually exclusive with `users` and `source`. Opaque JWTs cannot be patterned (that is [mint](#strategy-mint-self-issue-a-jwt-locally)'s job). The web console's "generate accounts from a pattern" panel (on the pool/login cards) does the same but materializes inline in the browser, so it is capped at 10,000 rows — for a larger pool use the scenario file's `usersPattern` (generated server-side).

> **Large pool files.** The external-source file cap is 512 MiB by default and parsed by streaming (a 300k-JWT JSONL of ~300–450 MB loads fine). `auth.source.maxBytes` moves the cap (positive only — the cap always stands). Login prewarm runs in parallel but bounded to `RateCap.MaxConcurrency` (at most 16) so it never load-tests the IdP.

#### Taking a web-authored run to the CLI

The browser cap is the signal to switch: when a pattern pool needs more than 10,000 rows (or you want the run in CI), carry the exact run you authored in the console over to a scenario file. The console's two JSON editors are already in scenario-file shape:

1. Copy the **Scenario graph** editor's JSON into a file's `graph:` block and the **API templates** editor's JSON into `templates:` (the [graph-first scenario form](#graph-first-scenario-files)); copy the **Start node** and **Max steps** fields into `start:` / `maxSteps:`.
2. Move the auth block: re-declare what the Auth card configured under `auth:` — for a pattern pool that is the `usersPattern` block, now free of the browser cap.
3. Raise `usersPattern.count` past 10,000 (the file path materializes it server-side at Expand).
4. `tmula run scenario.yaml`.

```yaml
target: http://localhost:9000        # the console's Base URL
start: browse                        # the console's Start node
maxSteps: 12                         # the console's Max steps
graph: { }        # ← paste the Scenario graph editor's JSON here
templates: { }    # ← paste the API templates editor's JSON here
auth:
  strategy: login
  usersPattern: { subject: "user{{.userIndex}}", token: "pw-{{.userIndex}}", count: 100000 }
  login:
    flow:
      - id: signin
        request: POST /auth/token
        body: '{"username":"{{.username}}","password":"{{.password}}"}'
```

Two differences from the console to remember: the file path **defaults the allowlist to the target's host** (the console makes you type it into both fields), and the pasted JSON is valid as-is inside the YAML file (YAML is a JSON superset).

### Strategy: login (mint a token at run time)

Use when the service issues tokens through a login endpoint:

```yaml
auth:
  strategy: login
  login:
    flow:
      - id: signin
        request: POST /auth/token
        body: '{"username":"u1","password":"p1"}'
        extract: { tok: "$.access_token" }
    capture:
      token: tok          # optional: which extracted variable becomes {{.token}} (empty/omitted = auto-detected)
      subject: uid        # optional: which variable becomes {{.subject}}
    scope: per-user       # per-user (default) | shared (client_credentials)
```

tmula walks the login flow once per virtual user (`per-user`) or once for all sessions (`shared`), captures the token, and sends it as `{{.token}}`. On a 401 mid-run it refreshes once (a real refresh-token exchange when one can be derived, else a re-login — see [What happens with a refresh token](#what-happens-with-a-refresh-token)) and retries; that refresh traffic is excluded from findings. Login runs are **in-process only** — rejected against a remote `--engine` and distributed workers.

> **Login prewarm and your IdP.** All per-user logins run up front (the *prewarm*), in parallel but bounded to the run's `RateCap.MaxConcurrency` capped at **16 concurrent logins** — so a 10,000-user run never turns into a login load test against your IdP. If the IdP still rate-limits, see the [FAQ entry on 429s during prewarm](#troubleshooting--faq).

#### Log in N distinct accounts (rows as credentials)

The block above logs in **one** identity. To make every virtual user a *different* account, add credential **rows** — for the login strategy a row is a login *input*: `subject` = username, `token` = password. Virtual user *i* logs in with row *i* (wrapping), and the login body references the row via `{{.username}}` / `{{.password}}`:

```yaml
auth:
  strategy: login
  source:
    file: users.csv         # rows of subject,token → username,password
    format: csv
  login:
    flow:
      - id: signin
        request: POST /auth/token
        body: '{"username":"{{.username}}","password":"{{.password}}"}'
    # capture omitted: the token is auto-detected from the response
```

No file? [`usersPattern`](#generate-accounts-from-a-pattern-userspattern) generates the rows instead — its `subject`/`token` templates become the username/password here (`user{{.userIndex}}` / `pw-{{.userIndex}}`), so "50,000 patterned accounts, each really logging in" is three lines of YAML.

#### client_credentials (shared machine token)

For a service principal (server-to-server) there is one identity for the whole run: a **form-encoded** `grant_type=client_credentials` exchange with `scope: shared`, so tmula logs in once and every session reuses that token:

```yaml
auth:
  strategy: login
  login:
    flow:
      - id: token
        request: POST /oauth/token
        headers: { Content-Type: application/x-www-form-urlencoded }
        body: "grant_type=client_credentials&client_id=REPLACE_ME_CLIENT_ID&client_secret=REPLACE_ME_CLIENT_SECRET"
    scope: shared
```

Add `&scope=read+write` when your IdP scopes the token. Some IdPs require extra parameters — **Auth0 requires `audience`** (`&audience=https://api.example.com`) or it returns an error instead of a token.

### Strategy: bootstrap-signup (provision real accounts)

Use when the test needs one real account per virtual user, provisioned for the run and torn down after:

```yaml
auth:
  strategy: bootstrap-signup
  signup:
    flow:
      - id: register
        request: POST /users
        body: '{"email":"tester@example.com"}'
        extract: { tok: "$.token", uid: "$.id" }
    capture:
      token: tok          # required
      subject: uid        # optional but needed for {{.subject}}-templated teardown
    teardown:
      - id: delete
        request: DELETE /users/{{.subject}}
```

Teardown runs after the load test, even if the run is killed or times out (best-effort on partial failure). If the signup block has no teardown flow, the run is **rejected** unless you pass `--keep-accounts` to opt out of teardown. Bootstrap runs are **in-process only** — rejected against a remote `--engine` and distributed workers. **Only use against a confirmed non-production sandbox.**

```bash
tmula run scenario.yaml --users 20                 # signup + run + teardown
tmula run scenario.yaml --users 20 --keep-accounts # signup + run, accounts left in place
```

### Strategy: mint (self-issue a JWT locally)

Use when the target **self-issues** JWTs and you hold the signing key: tmula signs a token per virtual user locally, no login. The key is a **reference** (env/file) only — its body is never serialized.

```yaml
auth:
  strategy: mint
  mint:
    alg: HS256                     # HS256 | RS256 | ES256
    secretEncoding: raw            # HS256: raw | base64 | base64url
    key: { env: MINT_SIGNING_KEY }  # or file: a local path (reference only)
    subject: "user-{{.userIndex}}"
    ttl: 1h
```

mint **can fan out to distributed workers** — only the key **reference** rides on the ShardSpec, and each worker resolves the same key locally and signs per global index. Prerequisite: the referenced key (env/file) must be deployed identically on every worker node; a worker that cannot resolve it fails its shard with a clear runtime error instead of running unauthenticated. **Caution:** mint cannot be used for a service whose signing key the IdP holds (Auth0/Cognito/Firebase) — the issued token would be rejected; use the login strategy or the web OAuth2 guide there. The web console shows a warning banner when you pick mint after importing such a managed-IdP spec.

### Strategy: exec (bring your own token — escape hatch)

Use when nothing above fits but a **local command** can print a token — a vendor CLI already mints them (`aws`, `gcloud`, `vault`, a custom SDK helper), or the auth dance is something tmula cannot model declaratively. tmula runs the command once per virtual user and its stdout becomes `{{.token}}`:

```yaml
auth:
  strategy: exec
  exec:
    command: ["vault", "token", "create", "-field=token"]  # argv - NOT a shell string
    env: { VAULT_ADDR: "https://vault.internal:8200" }     # secrets belong here, not in argv
    timeout: 10s            # optional per-invocation bound (a sane default applies)
    maxOutputBytes: 65536   # optional stdout cap (a sane default applies)
```

`command` elements and `env` values may reference `{{.userIndex}}`, so each virtual user can mint its own identity. The command is executed argv-only — **no shell**, so a metacharacter in an argument is passed literally.

**Security caveats — read before using:**

- exec runs an **arbitrary local command**. A scenario file alone never executes anything: the run is rejected unless the operator opts in with the **`--allow-exec`** flag (`tmula run --allow-exec`, or `WithAllowExec` on an embedded server). Off by default.
- The command's network egress is **outside the safety guard** — whatever it talks to is bound by neither the target allowlist nor the rate cap. Point it only at infrastructure you trust it with.
- Keep secrets in `env` values (which may reference host env), never in `command` — argv is visible to `ps`.

exec is **in-process only**: like login and bootstrap-signup it is rejected against a remote `--engine` and with distributed workers. Prefer any declarative strategy when one fits — exec is the escape hatch, not the default.

### Session-cookie auth

For a service whose login returns the credential as a `Set-Cookie` rather than a body token, leave the capture empty and the response's session cookie (a `session`/`token`/`jwt`/`auth`/`sid`-style name) is auto-captured as the credential secret. The scenario replays it as a Cookie header.

```yaml
auth:
  strategy: login
  login:
    flow:
      - id: signin
        request: POST /login
        body: '{"username":"u1","password":"p1"}'
    # capture omitted: with no token in the body, it is auto-detected from Set-Cookie
# and a scenario step header:
#   headers: { Cookie: "session={{.token}}" }
```

When the session expires and a request 401s, the login strategy's re-login fallback issues a fresh session and retries, so a static session pool self-heals like a token pool.

### Why secrets stay in-process

The domain `Credential.Secret` field carries `json:"-"`, so the secret is **never serialized**. It cannot cross the HTTP/SSE/store wire and is never persisted (its `String()` also redacts it in logs). Concretely:

- `tmula run` with an `auth:` block runs the control plane **in-process** (through its Go API) so the secret never has to be marshaled. The unauthenticated path still boots a real loopback engine over HTTP for parity.
- A `users[].cred` you try to POST over HTTP is **silently ignored**: the secret is stripped by `json:"-"`, so HTTP submission cannot carry auth.
- A remote `--engine` **refuses** an authenticated run (pool with inline users, login, or bootstrap): `a credential pool is not supported against a remote --engine (the secret cannot cross the wire); run in-process to authenticate`. An **external source** pool is the one exception: only a secret-free reference crosses the wire; workers resolve the file or env variable locally.

**Constraints** (from validation): a credential pool **cannot** be combined with distributed workers unless the pool is an external source or the mint strategy (both ship only a secret-free reference). Inline `users`, usersPattern, login, bootstrap-signup, and exec are all rejected with workers. The open workload model is in-process only regardless of auth strategy.

**What IS stored: login-flow bodies.** The `json:"-"` guarantee covers the *captured or minted* credential secret. It does **not** cover secrets you author into a flow body: a `client_credentials` client_secret, a pasted refresh token, or a username/password login body is part of the run spec like any other template body — it rides in the RunSpec, lives in engine memory for the run's lifetime, and is visible in the spec stored on the control plane (the web OAuth2 guide says as much next to its Client secret field). Keep the blast radius small: use a **throwaway test client** for client_credentials, keep per-user passwords out of the file with `--auth-source env:VAR` / an external `source`, or generate them with [`usersPattern`](#generate-accounts-from-a-pattern-userspattern) (the secret template materializes at Expand, and the materialized secrets are `json:"-"`-masked).

### Web OAuth2 guide · basic/apiKey import · what's deferred

- **OAuth2 web guide.** For a service that only speaks OAuth2, the web console's "It's an OAuth2 service" entry assembles the login flow for you: answer the token URL and how you log in (username/password · client key · paste a refresh token · access token only). For services that need a human consent screen (Auth0/Cognito/social login), the right path is to copy a refresh token once from the app/devtools and paste it. (Note: a client_secret or pasted refresh token is stored in the run's spec like any login body, so prefer a throwaway test client for client_credentials.)
- **basic auth / apiKey import.** Importing an OpenAPI/Swagger spec derives http `basic` (→ an `Authorization: Basic {{basicAuth .subject .token}}` header + a username/password pool), `apiKey` in query (→ `?name={{.token|urlquery}}` appended to the path), and `apiKey` in cookie (→ a `Cookie: name={{.token}}` header). Fill the `REPLACE_ME` and run. An `openIdConnect` scheme surfaces its discovery URL as an advisory; the web points you to paste its token_endpoint into the OAuth2 guide.
- **PKCE / device-code are deferred.** The human-consent authorization-code + PKCE and device-code flows (in-app browser, social, MFA) are **not supported** — they aren't a headless-automation target. Workarounds: the OAuth2 guide's **paste-a-refresh-token** path (log in as a human once, copy the refresh token), a pre-issued token pool, or [exec](#strategy-exec-bring-your-own-token--escape-hatch) (a bring-your-own-token command) as a last resort.

#### What happens with a refresh token

When the login is an OAuth2 **form grant** (`grant_type=…` in a form-encoded body), tmula auto-derives a real refresh exchange from it: a `grant_type=refresh_token&refresh_token={{.refreshToken}}` POST to the same token endpoint (an explicit `login.refresh` override wins over the auto-derivation and works for JSON-body logins too). The refresh token is captured from the login response at run time and never authored in the file. The lifecycle in practice:

- **The 401 one-retry interplay.** Mid-run, a request that 401s triggers **one** refresh — the derived `refresh_token` exchange when there is one, else the re-login fallback — and one retry of the failed request. The refresh/re-login traffic is excluded from findings.
- **Per-user vs shared scope.** With `scope: per-user` every virtual user holds its own access + refresh token pair and refreshes independently. With `scope: shared` (client_credentials style) there is one credential: a single refresh rotates the token every session uses.
- **Rotation-on-use.** Many IdPs (Auth0 with rotation enabled, Cognito, most consumer services) **rotate the refresh token on every exchange** — the response carries a *new* refresh token and invalidates the old one. tmula handles this: each exchange stores the rotated (or carried-forward) refresh token for the next one. But it also means the refresh token you pasted into the web OAuth2 guide may be **single-use**: don't reuse the same pasted value in a second concurrent run, and expect the app you copied it from to be logged out once tmula's first refresh consumes it.

---

## Distributed mode

For very large runs you can fan out across machines. One process is the **master** (serves the control plane + UI); others are **workers** (serve a gRPC service). The master dials each worker, splits the virtual users into shards, and aggregates their streamed results identically to the local path.

```bash
# on each worker box
tmula --role worker --addr :9101

# on the master, naming the worker pool
tmula --role master --addr :8080 --workers 10.0.0.5:9101,10.0.0.6:9101
```

You can also set workers per-experiment via the RunSpec `workers` field (or the console's **Workers** field).

**`aggregateWorkers` and the fidelity trade-off.** By default each worker streams every request back, and the master classifies findings per endpoint with run-length availability detection (full fidelity). With `aggregateWorkers: true`, each worker folds its whole shard into a mergeable **Summary** (counters + a bounded-memory latency histogram) and the master merges those. Network and memory stay bounded even at millions of requests, but findings become **run-wide per category** (one finding per tripped category, no per-endpoint breakdown, no consecutive-failure streaks). Use it when the request volume would overwhelm streaming; keep it off when you want per-endpoint, run-length findings.

**When to use distributed mode at all:** only when a single box cannot generate (and the SUT cannot serve) the load you need. The open workload model is **in-process only**: distributed workers are rejected for open runs (`api: distributed workers are not supported with the open workload model`), and a credential pool is rejected with workers too.

---

## Safety

Because tmula deliberately concentrates traffic, a misfire would be a self-inflicted outage. Three guards (`server/internal/safety`) make that hard to do by accident; every outbound request passes all three.

- **Allowlist.** A host allowlist (`TargetEnv.Allowlist`): the only hosts a run may reach. A request whose host is not on the list is blocked (`safety: host "<h>" not in allowlist`). Patterns are exact, or a leading `*.` wildcard (`*.example.com`). The allowlist must be non-empty; there is no "reach anything" mode.
- **Rate cap.** A hard ceiling: `rateCap.maxRps` (a token bucket, burst capped at one second of rate) and `rateCap.maxConcurrency` (in-flight ceiling). Both must be `> 0`. Exceeding either yields a `LimitError` for that request rather than overrunning the target.
- **Kill switch.** Always-on **manual** stop (the console's **Kill run** button), plus an opt-in **automatic** trip: when a *rolling* error rate over the most recent N outcomes exceeds a threshold, the guard trips itself (`auto: rolling error rate X over last N exceeded threshold Y`). The automatic trip is **disabled by default** so saturation can be observed.
- **Environment class.** `envClass` is `dev`, `staging`, or `prod-locked`. A `prod-locked` target is refused unless explicitly unlocked (`safety: target env is prod-locked; explicit unlock required (policy §1)`).

Together these mean a run cannot reach a host you did not list, cannot exceed the rate/concurrency you set, can always be stopped, and cannot accidentally hit production.

For how **credentials** are treated — what never serializes, and what *does* ride in a run spec (login-flow bodies such as a client_secret or password) — see [Why secrets stay in-process](#why-secrets-stay-in-process) and its "What IS stored" caveat. Note also that the [exec strategy](#strategy-exec-bring-your-own-token--escape-hatch)'s command egress runs outside these guards, behind its own `--allow-exec` opt-in.

---

## The example domains

Two runnable demos double as a catalog of *deliberately planted bugs*, so you know what findings to expect when you point tmula at them. Pick one as a web **preset** (it fills the scenario *and* the target) or run it from the CLI.

### shop - `server/examples/sample-api` (`:9000`)

Journey: `browse → search / category → product → cart → checkout → done`. Planted bugs:

| Endpoint | Planted behavior | Expected finding |
|----------|------------------|------------------|
| `GET /browse` | Healthy, ~3 ms | none |
| `GET /search` | ~5% of responses sleep ~180 ms - a latency tail | shows up in **p95/p99** (threshold if gated) |
| `GET /category` | Healthy, ~5 ms | none |
| `GET /product` | ~2% return **404** - a rare broken product link | **contract** |
| `POST /cart` | ~8% return **500** - an intermittent "cart hiccup" | **contract** |
| `POST /checkout` | ~8% baseline failures that **climb with concurrent load**, capped at 40% (503) - degraded under pressure, never fully down, recovers when load eases | **contract** + elevated **threshold** error rate under load |

### ticketing - `server/examples/ticketing-api` (`:9100`)

Journey: `events → detail → seats → hold → pay → done`. Planted bugs:

| Endpoint | Planted behavior | Expected finding |
|----------|------------------|------------------|
| `GET /events` | Healthy, fast | none |
| `GET /events/{id}` | ~3% return **404** (sold out / removed) | **contract** |
| `GET /seats` | ~6% slow (~150 ms) - a popular show's latency tail | **p95/p99** |
| `POST /hold` | ~18% return **409** - seat contention (another buyer grabbed it first) | **contract** |
| `POST /pay` | Degrades under the on-sale rush; failure climbs with concurrent load, capped at 40% (503) | **contract** + elevated **threshold** error rate |

The `409`s on `hold` and the `503`s on `pay` concentrate on those specific endpoints, the pattern of a real planted bug (see the FAQ on endpoint-concentrated errors).

A hands-on "0 to 100" walkthrough (in Korean) lives at [`examples/USAGE.md`](../examples/USAGE.md); it is a companion to this reference, not a duplicate.

---

## Troubleshooting & FAQ

**Q: The console is empty / shows a "run `make web`" placeholder page.**
You built without embedding the UI. A plain `make build` / `go build` ships a placeholder. Run `make web` (or use the Docker image / prebuilt binary, which already embed the real console) and reload <http://localhost:8080>.

**Q: My run is "all errors", every request failed.**
The target host is not in the **Allowlist**. The web console does **not** auto-add the Base URL host to the allowlist (`buildRunSpec` only trims/splits the field), and the safety guard blocks anything off the list. Put the target host in *both* the Base URL and the Allowlist. (The scenario-file and CLI paths default the allowlist to the target host; only the web console makes you type it twice.)

**Q: Under Docker, the run still fails to reach the API.**
Inside the Compose network the engine reaches the SUTs by **service name**, not `localhost`. Set the Base URL to `http://sample-api:9000` (shop) or `http://ticketing-api:9100` (ticketing) and put `sample-api` / `ticketing-api` in the Allowlist. `localhost` inside the engine container points at the engine itself, not the SUT.

**Q: I get `decode: http: request body too large`.**
You shipped one object per virtual user for a huge run, and the request body overflowed the server's size limit. Don't materialize per-user objects: for a closed run send an empty `users: []` plus a `userCount` (the server synthesizes `u0..uN-1`), or use the open model (it generates its own sessions from the arrival rate). The web console already does this for you.

**Q: `virtualUserCount must be > 0` on an open run, but the open model doesn't use a user count?**
Correct, it's a *nominal* field, but it's still validated `> 0` regardless of workload model. Set any positive number (the scenario-file path sets it to `1` automatically). It does not change open-model behavior.

**Q: Huge error rates, a bimodal latency (p50 ≈ 0, p95 in seconds), and lots of timeouts. Is the machine broken?**
No, that's the signature of **overload / saturation**, usually because you are *generating* the load and *serving* the SUT on the **same box with no concurrency cap**. The fast successes give p50 ≈ 0 while the saturated tail pushes p95 into seconds, and the timeouts pile up. Fix it by setting **Max concurrency** (open model) and/or lowering the **Arrival rate**, or by moving the system under test onto a separate machine. This is a measurement artifact of your harness, not a bug in the SUT.

**Q: The errors concentrate on a few specific endpoints.**
That is usually a *real* bug in the system under test, and in the example domains, a deliberately planted one. The ticketing `POST /hold` 409s (seat contention) and `POST /pay` 503s (payment under rush), or the shop `POST /cart` 500s and `POST /checkout` degradation, are the kind of endpoint-concentrated failure tmula is built to surface. Endpoint-concentrated errors → look at that endpoint; run-wide errors with saturation symptoms → look at your harness (previous question).

**Q: A CI run exited 2 even though I expected a pass.**
`--fail-on-findings` (or `--fail-on-severity`) intentionally exits `2` when findings are detected. That's the gate working. A `3` means the [baseline gate](#the-baseline-gate--fail-only-on-regressions) found findings *new* vs the baseline (after known-issue suppression). A `1` instead means an actual error or a failed/killed run (e.g. a timeout or kill-switch trip), which always exits non-zero regardless of findings. `0` means clean - which, with a baseline, includes "only persisting findings."

**Q: My authenticated run "works" against a remote `--engine` but carries no token.**
It can't, and the run path refuses it: a credential pool against a remote `--engine` is rejected because the secret cannot cross the wire (`json:"-"`). Run in-process (`tmula run` with an `auth:` block, no `--engine`) to authenticate.
