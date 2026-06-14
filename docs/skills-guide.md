# tmula skills - full guide

*English · [한국어](skills-guide.ko.md)*

A walkthrough of the five [Claude Code](https://docs.claude.com/en/docs/claude-code) skills that take you
from "here's an API" to "here's what breaks under load" with tmula. For the one-screen overview see
[skills.md](skills.md); this is the detailed reference - what each skill does, how it's used, its process,
and what its output looks like (with real output captured from a live run).

---

## Overview

```
  source                json/scenario.json                 run                findings
 (spec/HAR/log) ─▶ tmula-scaffold ─▶ tmula-enrich ─▶ tmula-run ─▶ tmula-triage
                          │               │              │             │
                          └───────────────┴──── tmula-up (orchestrates all four) ────┘
```

Four **atoms** - each works standalone with no dependency on the others - and one **orchestrator** that
chains them. They live in `.claude/skills/` and Claude Code auto-discovers them when you open this repo.

**Invoke** a skill by its slash name (`/tmula-up`, `/tmula-scaffold`, …) or just describe the goal
("load-test this API from its swagger", "make my scenario runnable", "is this finding real?").

**Artifact contract** (this is how the atoms stay independent - they pass *files*, not control):

| Artifact | Produced by | Consumed by |
|---|---|---|
| `json/scenario.json` | scaffold (writes), enrich (rewrites) | enrich, run, up |
| `json/report.json` (`tmula run --json`) | run | triage (as `--baseline-file`) |
| `./bin/tmula` | `go build -o ./bin/tmula ./server/cmd/tmula` | all |

Each atom takes input from an explicit path → else the conventional file (`json/scenario.json` / `json/report.json`)
→ else it asks. So none requires another to have run first; `tmula-up` is pure convenience.

**Safety model.** Running a scenario sends **real HTTP traffic**. The hard gate lives in `tmula-run` (and is
restated by `tmula-up`): the target must be confirmed **non-production**, the flow is **read-only by
default**, and DELETE/mutating ops need explicit opt-in. Fetched specs/logs are treated as **untrusted
data**. tmula's own defaults back this up: `envClass=dev`, `allow` = target host (fail-closed),
`rateCap` = `{maxRps:10000, maxConcurrency:1000}`. A **guard hook** (`.claude/hooks/tmula-guard`, wired in
`.claude/settings.json`) backs this up deterministically: it **blocks any `tmula run`** whose target is not
loopback/private, or whose scenario file is missing. Opt in to a real sandbox with
`export TMULA_ALLOW_TARGET="host"` or a `.tmula-allow` file. (Hooks load at session start.)

---

## 1. tmula-scaffold - source → starting scenario

**What.** Converts an API description into a *raw* `json/scenario.json` and resolves the target URL. Input may be
an **OpenAPI 3 / Swagger 2** doc (URL or file), a **HAR** capture, or an **access log** - or just a running
**API's base URL** if it serves a spec (the helper probes `/openapi.json`, `/v3/api-docs`, … and follows a
Springdoc `swagger-config`), so `http://host` alone can work.

**When.** You have a spec/log and want a tmula scenario to start from. *"Turn my swagger into a tmula
scenario."* It does **not** send traffic.

**Process.**
1. Build the binary (once); run everything from the repo root or via `REPO=$(git rev-parse --show-toplevel)`.
2. Resolve the input to a local spec + default target - the tested helper handles URLs, HTML Swagger-UI
   pages, and Springdoc `swagger-config`:
   ```bash
   eval "$(./.claude/skills/tmula-scaffold/scripts/fetch-openapi.sh <url>)"   # → SPEC=… TARGET=…
   ```
3. Confirm the target. If the spec's server is **relative** (`/api/v3`) or absent, `tmula init` errors and
   the target is derived from the Swagger URL's own origin.
4. Import: `./bin/tmula init --from "$SPEC" --target "$TARGET" --out /tmp/scenario.yaml`.
5. Convert to JSON: `python3 -c "import yaml,json; json.dump(yaml.safe_load(open('/tmp/scenario.yaml')), open('json/scenario.json','w'), indent=2)"`.

**Format notes.** OpenAPI/Swagger → linear `flow` in a *journey-heuristic* order (auth → browse → view →
cart → pay; **not** spec order, mutating ops interleaved). HAR → linear `flow` in capture order. Access log →
a **graph-first** scenario (`graph`+`templates`+`start`+`open`) with weights/arrival-rate learned from the
traffic.

**Result** (real, from `examples/imports/shop.openapi.yaml`):

```json
{
  "target": "http://127.0.0.1:9100",
  "flow": [
    { "id": "browse",   "request": "GET /browse" },
    { "id": "category", "request": "GET /category" },
    { "id": "search",   "request": "GET /search" },
    { "id": "product",  "request": "GET /product" },
    { "id": "addToCart","request": "POST /cart",     "body": "{\"productId\":\"p7\",\"qty\":1}" },
    { "id": "checkout", "request": "POST /checkout", "body": "{\"total\":42}" }
  ]
}
```

It is a **raw scaffold**: path params are literal (`{id}`), there's no auth, and destructive ops are
present. Next: tmula-enrich.

**For the web console (`:8080`):** scaffold can also emit the console's edit-field JSON - run
`to-console.sh <engine-url> <source> [format]` (it POSTs the source to a running engine's `/api/import`) to
write `json/graph.json` (→ *Scenario graph* field) + `json/templates.json` (→ *API templates* field) and
print the *Start node* / *Max steps*; the raw source goes to `json/source.<ext>` for the *Import* field.

---

## 2. tmula-enrich - raw scenario → runnable & safe

**What.** Applies the judgment a raw import can't: substitute literal path params, wire auth headers, **filter
out destructive operations**, and shape the flow (deps / weights / finding thresholds).

**When.** You have a `json/scenario.json` (from scaffold, hand-written, or pasted) that won't run cleanly or
safely yet. It does **not** send traffic.

**Process.**
1. Read the scenario (linear `flow` or graph `templates` - both support `headers`).
2. **Substitute every `{path-param}`** - `example` → `enum[0]` → by type (integer→`1`, uuid→fixed, string→
   `"string"`). `GET /pet/{petId}` → `GET /pet/1`. A leftover brace silently 404s.
3. **Safety filter**: drop `DELETE` and mutating `POST`/`PUT`/`PATCH` by default; keep them only on explicit
   opt-in. (Petstore v3 has 11 such ops to drop.)
4. **Wire auth** if the spec declares security: the credential token is exposed only as the Go template var
   `{{.token}}` (with the dot), and the pool injects nothing unless a step references it:
   - API-key header → `"headers": { "api_key": "{{.token}}" }`
   - OAuth2 / bearer → `"headers": { "Authorization": "Bearer {{.token}}" }`

   plus an `auth` pool with a labeled placeholder. `{{.token}}` is always the static pool token - a `login`
   step does **not** supply it.
5. Optionally set `dependsOn`, `weight`, `deviationRate`, `findings` thresholds, `users`.

**Result** (the shop scenario, auth-free; for an authed API enrich would add the `auth` block + headers):
path params filled, destructive ops dropped, optional `findings` thresholds added. Report to the user
exactly what was filled, wired, and dropped. Next: tmula-run.

---

## 3. tmula-run - scenario → load test (safely)

**What.** Executes the scenario against its target and reports findings. **This is the step that sends real
traffic**, so it enforces the non-prod safety gate, dry-runs with one user, then runs the real load.

**When.** You have a runnable `json/scenario.json` and want to actually load-test the target.

**Safety gate (clear all three first).** ① target confirmed non-production · ② no unintended destructive ops
(read-only default) · ③ `--users 1` is still real traffic.

**Process.**
1. Clear the safety gate.
2. **Dry-run (1 user)** - the smallest real run; tmula has no zero-traffic mode:
   ```bash
   ./bin/tmula run json/scenario.json --users 1 --timeout 30s --fail-on-findings
   ```
   Exit code alone is **not** proof - a plain run exits 0 even at 100% errors; `--fail-on-findings` /
   `--fail-on-severity` make it meaningful. Always READ the printed summary.
3. **Real load** (closed or open model):
   ```bash
   ./bin/tmula run json/scenario.json --users 50                 # closed: 50 concurrent users
   ./bin/tmula run json/scenario.json --open 278 --for 3600      # open: 278 arrivals/sec
   ```
4. Report `requests / errors / p50·p95·p99 / status counts / findings` honestly.

**Run it in the web console (the web environment).** Point the run at a live engine instead of in-process -
it executes server-side and is **retained**, so the browser console can attach to it:

```bash
make web                                                    # one-time: build + embed the React console
./bin/tmula --addr :8080                                    # engine + console (bare; NO `serve` subcommand)
./bin/tmula run json/scenario.json --engine http://localhost:8080 --users 1   # dry-run, server-side
#   → open http://localhost:8080/?run=<run-id>
```
(An authed scenario's credential pool can't cross the wire to a remote `--engine` - run those in-process.)

**Result** (real, server-side against the bundled sample-api):

```text
# dry-run — 1 user
run id   : run-2 · completed
requests : 6 · errors: 0 · status: {200:6}
findings : 0                       # clean: the shop's planted bugs are load-dependent, 1 user got lucky

# real load — 50 users
run id   : run-4 · completed
requests : 300 · errors: 19 · status: {200:281, 404:1, 500:3, 503:15}
findings : 2
  • [CRITICAL] contract : 3 contract violation(s) on addToCart
  • [CRITICAL] contract : 15 contract violation(s) on checkout
```

A clean dry-run that turns red under load is the normal, useful signal - that's what tmula is for. Next, if
findings appeared: tmula-triage.

**Exit codes.** `0` ok · `1` run error · `2` any finding (with `--fail-on-findings`/`--fail-on-severity`) ·
`3` new findings vs `--baseline`.

---

## 4. tmula-triage - finished run → what it means

**What.** After a run finds something: reproduce a finding in isolation (functional vs load-dependent), gate
against a baseline, and wire it into CI.

**When.** A run produced findings and you want to know which are real, whether anything regressed, or how to
make load testing a CI gate.

**A. Reproduce** (sends a small replay - confirm the target is the same non-prod sandbox first). Needs the run
held by a **live engine** (in-process runs aren't retained):

```bash
./bin/tmula run json/scenario.json --engine http://localhost:8080 --users 50    # run on the engine → run-N
./bin/tmula reproduce --engine http://localhost:8080 --run run-N --finding contract/checkout --attempts 5
```

Verdict: **functional** (fails every attempt alone → real bug) · **load-dependent** (never fails alone →
concurrency/saturation) · **flaky** (fails some). The verdict is a *signal, not proof* (it can't recreate the
original timing/concurrency).

**Result** (real, reproducing the shop's checkout finding):

```text
Verdict: load-dependent — reproduced 0/5 attempts under no load → likely concurrency/saturation
```

→ chase resource limits, not the handler. (The cart finding reproduced intermittently → flaky; raise
`--attempts`.)

**B. Baseline gate** - fail only on **new** findings vs a known-good run:

```bash
./bin/tmula run json/scenario.json --json > json/report.json                 # capture a known-good baseline
./bin/tmula run json/scenario.json --baseline-file json/report.json          # exit 3 ONLY on new findings
```

> Do **not** add `--fail-on-findings` to a baseline gate - the absolute gate (exit 2) preempts the regression
> gate (exit 3), so the "new only" comparison is silently ignored. Use the baseline flags alone for a
> regression gate; use `--fail-on-findings` alone to fail on any finding.

Suppress known issues with a mandatory expiry: `--known-issues known-issues.yaml` (entries carry
`expires: YYYY-MM-DD`; past that they re-surface).

**C. CI** - pick one gate; the repo ships a composite GitHub Action (`action.yml`) that runs the gate and
upserts a PR comment.

---

## 5. tmula-up - the whole pipeline, end to end

**What.** Orchestrates **scaffold → enrich → run → triage** with confirmation gates, resuming wherever you
already are.

**When.** *"Do the whole thing from this URL."* / *"tmula 한 방에 돌려줘."*

**Stage detection.** A spec/HAR/log → start at scaffold; a raw scenario → enrich; a scenario the user calls
"enriched" → re-inspect (grep for literal `{param}` and mutating verbs) then run; a finished run → triage.

**Process.** scaffold (confirm target) → enrich (fill/wire/filter) → **hard safety gate** (non-prod + which
ops run; non-skippable) → run (dry-run then load; `--engine` for the web console) → triage (if findings) →
end-to-end summary.

**Result.** A single narrative: source → target → scenario shape → what was filtered for safety → load
result → triaged verdicts → suggested next step.

---

## Reference

### json/scenario.json schema

Required: `target` plus either `flow` (linear) or `graph`+`templates`+`start` (branching, from access-log
import). Per linear step: `id`, `request` (`"METHOD /path"`), `body`, `headers` (values may use `{{.token}}`
/ `{{.subject}}`), `dependsOn` (a never-skipped prerequisite edge), `weight` (edge probability, default 1).
Top-level optional (defaults): `allow` (= target host, fail-closed) · `users` (closed model, default 20) ·
`open`+`maxSteps` (open arrival model) · `seed` (1) · `deviationRate` (0) · `auth` (pool only in-file) ·
`findings` (`errorRate` 0.2 / `p95LatencyMs` disabled when omitted / `availabilityStreak` 5).

### End-to-end example (exactly what was run for this guide)

```bash
REPO=$(git rev-parse --show-toplevel); cd "$REPO"
go build -o ./bin/tmula ./server/cmd/tmula
( cd server && go build -o /tmp/sample-api ./examples/sample-api )         # a safe local target
SAMPLE_API_ADDR=127.0.0.1:9100 /tmp/sample-api &                           # the SUT

# scaffold
./bin/tmula init --from examples/imports/shop.openapi.yaml --target http://127.0.0.1:9100 --out /tmp/s.yaml
mkdir -p json
python3 -c "import yaml,json; json.dump(yaml.safe_load(open('/tmp/s.yaml')), open('json/scenario.json','w'), indent=2)"

# web environment
make web && ./bin/tmula --addr :8090 &
./bin/tmula run json/scenario.json --engine http://127.0.0.1:8090 --users 1  --timeout 30s   # dry-run → run-2, all 200
./bin/tmula run json/scenario.json --engine http://127.0.0.1:8090 --users 50 --timeout 40s   # load   → run-4, 2 findings
#   open http://127.0.0.1:8090/?run=run-4   ← live traffic-flow map + findings
```

### Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| init *"could not determine a target URL"* | relative/absent server | derive target from the Swagger URL origin; pass `--target` |
| requests 404 with `{param}` in path | path params not substituted | tmula-enrich; substitute every `{...}` |
| protected endpoints 401/403 | no `{{.token}}` header / unfilled token | wire a `headers` block; fill the pool placeholder |
| `template: …` error / request won't send | used `{{token}}` without the dot | it must be `{{.token}}` |
| run exits 0 despite errors | plain run | add `--fail-on-findings` / `--fail-on-severity`; READ the summary |
| baseline gate fails on a known issue | combined with `--fail-on-findings` | drop it (exit 2 preempts exit 3), or suppress via `--known-issues` |
| `reproduce` can't find the run | run wasn't on a live `--engine` | re-run with `--engine`, then reproduce |
| console at `/` is a placeholder | binary built without the UI | `make web` (or `make embed`) to embed the React console |
| `import yaml` fails | PyYAML not installed | `pip install pyyaml`, or keep `scenario.yaml` (tmula reads either) |
