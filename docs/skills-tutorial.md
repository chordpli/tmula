# Setting up tmula with a Claude skill

*English · [한국어](skills-tutorial.ko.md)*

A real session record of load-testing a shop API built for demos, end to end, with a single call to the
orchestrator `/tmula-up`. Every command and output is from an actual run. For the per-skill detail
see [skills-guide.md](skills-guide.md).

## The pipeline's skills

| Skill | What it does | Command it runs |
|---|---|---|
| **tmula-scaffold** | build a scenario scaffold from a spec (+ console graph/templates); auth is auto-derived when the spec declares a security scheme | `tmula init`, `to-console.sh` |
| **tmula-auth** | derive the `auth` block standalone (login flow + headers + one `REPLACE_ME` secret) — the demo shop below needs none | `tmula init` (auth derivation) |
| **tmula-enrich** | make the scenario runnable and safe (no CLI command; edits the file) | (none) |
| **tmula-run** | safety gate → dry-run → load | `tmula run` |
| **tmula-triage** | reproduce a finding · baseline gate | `tmula reproduce`, `tmula run --baseline-file` |
| **tmula-up** | run the pipeline skills above, in order | (calls the skills above) |

All generated JSON is written under a `json/` folder (gitignored): scenario, report, baseline, plus the web
console's `graph.json` · `templates.json` · `source.json`.

---

## 0. Setup (once)

The load target is the repo's shop API built for demos (`server/examples/sample-api`). It is a server with
deliberately planted bugs, so load surfaces problems in the cart and checkout.

```bash
REPO=$(git rev-parse --show-toplevel); cd "$REPO"
make embed                                                   # embed the React console into bin/tmula
make shop &                                                  # demo shop SUT (:9000, the load target)
./bin/tmula --addr 127.0.0.1:8090 &                          # tmula engine + web console
# check: shop :9000 200 · engine :8090 200
```

Like a real service, the demo API serves its own OpenAPI spec at `GET /openapi.json` (the server URL is the
relative path `/`). So a bare URL is enough: the scaffold helper finds `/openapi.json`, fetches the spec,
and derives the target. There is no need to hand it a local file. For an API that serves no spec, provide a
spec file, a HAR, or an access log instead.

---

## The call

```
/tmula-up localhost:9000
```

Along with the skill command, only the API's URL was given. tmula-up proceeds in order: scaffold (auto-discover the spec) → enrich → safety gate →
run → triage. The six stages below are the real results.

---

## [1/6] scaffold

Discover the spec from the server with the helper, build a scaffold with `tmula init`, and save it to
`json/scenario.json`. When the web console is used, also generate the JSON for its edit fields.

```bash
eval "$(.claude/skills/tmula-scaffold/scripts/fetch-openapi.sh http://localhost:9000)"
# → resolved: http://localhost:9000/openapi.json ·  SPEC=/tmp/...json  TARGET=http://localhost:9000
mkdir -p json
./bin/tmula init --from "$SPEC" --target "$TARGET" --out /tmp/up.yaml
python3 -c "import yaml,json; json.dump(yaml.safe_load(open('/tmp/up.yaml')), open('json/scenario.json','w'), indent=2)"
```

Only a URL was given, but the helper found `/openapi.json`, and because the server is relative it derived
the target `http://localhost:9000`. **6 steps.** No path params, no auth, mutating ops `addToCart`·`checkout`
present. It is still a scaffold before any shaping.

**Web console (:8080) artifacts.** The console's manual-edit fields take *graph* and *templates*
separately, not the compact scenario. Generate them in that exact shape via the same `/api/import` (an
engine must be running).

```bash
cp "$SPEC" json/source.json                                  # for the console's "Import" field
./.claude/skills/tmula-scaffold/scripts/to-console.sh http://localhost:8090 "$SPEC" auto
# → wrote json/graph.json + json/templates.json  (Start node: 'browse' · Max steps: 6)
```

| Console field | File |
|---|---|
| Scenario graph | `json/graph.json` |
| API templates | `json/templates.json` |
| Import from OpenAPI / HAR / access log | `json/source.json` |

These graph/templates are the same baseline the console's Import produces. Pasting them into the fields and
editing there in the console corresponds to enrich on the CLI.

---

## [2/6] enrich (file editing, no CLI command)

Fill path params, drop mutating ops by default but keep them - opted in - only for a confirmed sandbox, and
wire auth via `{{.token}}` headers if any. shop has no params or auth, so the crux is the safety decision.

By the rules, `json/scenario.json` is rewritten:

- path params: none → nothing to substitute · auth: none → nothing to wire
- mutating `cart`·`checkout`: normally dropped, but confirmed as "a local demo sandbox with the checkout
  flow as the test target" → kept (opt-in)
- shape: `dependsOn: addToCart` on `checkout`, finding thresholds, `users: 30`

```json
{
  "target": "http://localhost:9000",
  "flow": [
    { "id": "browse",    "request": "GET /browse" },
    { "id": "category",  "request": "GET /category" },
    { "id": "search",    "request": "GET /search" },
    { "id": "product",   "request": "GET /product" },
    { "id": "addToCart", "request": "POST /cart",     "body": "{\"productId\":\"p7\",\"qty\":1}" },
    { "id": "checkout",  "request": "POST /checkout", "body": "{\"total\":42}", "dependsOn": "addToCart" }
  ],
  "users": 30,
  "findings": { "errorRate": 0.05, "p95LatencyMs": 800, "availabilityStreak": 5 }
}
```

enrich has no matching CLI command. Shaping the scenario is done as rule-based file editing. cart·checkout
survived only because the sandbox was confirmed; without that confirmation both ops are dropped.

---

## [3/6] Safety gate (non-skippable)

Before any traffic, confirm the target is not production and that the mutating ops are opted in.

Cleared: `localhost:9000` is a local loopback demo (non-prod), and the mutating ops `cart`·`checkout` are
opted in. The guard hook also allows loopback targets.

---

## [4/6] run - dry-run → load on the web engine

First a dry-run with 1 user, then the real load. Because `--engine` is used, the run executes server-side
and is retained.

```bash
./bin/tmula run json/scenario.json --engine http://localhost:8090 --users 1  --timeout 30s --fail-on-findings
./bin/tmula run json/scenario.json --engine http://localhost:8090 --users 30 --timeout 40s --json > json/report.json
```

**dry-run (1 user):**

```text
Run run-22 — completed · local
  requests=6  errors=1 (16.7%)  status: 200:5  500:1
```

This dry-run is not clean. cart's ~8% probabilistic 500 occurred once even at 1 user × 6 requests. A clean
dry-run does not guarantee safety, and the reverse also holds. A probabilistic bug occurs intermittently at
low load too. (Exit code is 2 because of `--fail-on-findings`.)

**real load (30 users):**

```text
Run run-24 — completed · local
  requests=180  errors=14 (7.8%)
  status: 200:166, 500:4, 503:10
  findings:
    • [CRITICAL] contract  : 4 contract violations on addToCart
    • [CRITICAL] contract  : 10 contract violations on checkout
    • [WARNING]  threshold : error rate 0.08 (over the 0.05 threshold)
```

Under load more failures appeared: `addToCart` 500s, `checkout` 503s. You can see them on the console's flow
map: `http://localhost:8090/?run=run-24`. Two CRITICALs, so move on to triage.

---

## [5/6] triage - classify the cause + baseline gate

Reproduce each finding in isolation under no load (needs an engine-retained run), and report the verdict as
"a signal, not proof". Do not use the baseline gate together with `--fail-on-findings`.

```bash
./bin/tmula reproduce --engine http://localhost:8090 --run run-24 --finding contract/checkout  --attempts 10
./bin/tmula reproduce --engine http://localhost:8090 --run run-24 --finding contract/addToCart --attempts 10
```

```text
checkout  → Verdict: flaky          — reproduced 1/10 under no load
addToCart → Verdict: load-dependent  — reproduced 0/10 under no load
```

The verdict is a signal, not proof. addToCart was classified `load-dependent` (0/10), but in the code cart's
500 is a load-independent ~8% probabilistic bug. Across 10 attempts an 8% chance can land on 0 (it fired
even in the 1-user dry-run), and raising `--attempts` converges it toward functional. checkout's 503 stems
from concurrency, so it wobbles on the `flaky`/`load-dependent` boundary.

**baseline gate:**

```bash
./bin/tmula run json/scenario.json --engine http://localhost:8090 --users 30 --json > json/baseline.json
./bin/tmula run json/scenario.json --engine http://localhost:8090 --users 30 --baseline-file json/baseline.json
```

```text
Baseline gate vs run-26: 0 new · 0 resolved · 3 persisting · 0 suppressed
exit code: 0
```

This time there are no new findings, so exit code 0 (passes), and the three known findings are `persisting`.
On a probabilistic target, though, a finding absent at baseline-capture time can be classified `new` on the
next run (exit code 3 then). In that case, capture a representative baseline or fold them into
`--known-issues`.

---

## [6/6] end-to-end summary

| Stage | Result |
|---|---|
| source | `localhost:9000` (URL only - spec auto-discovered) |
| scaffold | `http://localhost:9000`, 6 steps → `json/scenario.json` (+ console `graph.json`/`templates.json`/`source.json`) |
| enrich | cart/checkout kept on sandbox opt-in · `checkout dependsOn addToCart` · findings · users=30 |
| safety gate | cleared (local loopback non-prod + writes opted in) |
| run (dry-run, 1) | run-22 · 6 requests · 1 error (cart 500, the probabilistic bug at 1 user) |
| run (load, 30) | run-24 · 180 requests · 7.8% errors · `addToCart`·`checkout` CRITICAL |
| triage reproduce | checkout=flaky(1/10) · addToCart=load-dependent(0/10) - the verdict is a signal |
| baseline gate | `0 new · 3 persisting` → exit 0 (passes) |

Artifacts: `json/scenario.json` · `json/report.json` · `json/baseline.json` · console `json/graph.json` ·
`json/templates.json` · `json/source.json` (all gitignored). Live console: `http://localhost:8090/?run=run-24`.

---

## Summary

1. scaffold only builds the basic scaffold. Enrich it to make it run as intended.
2. Because a run can affect a real production environment, safety is the default. Risky ops are dropped by
   default, and you include them yourself only for your own sandbox.
3. A dry-run does not guarantee safety. The real problems mostly occur under load, but with low probability a
   bug occurs intermittently at low load too.
4. reproduce separates the cause. The verdict is a signal, not proof, so with a small sample it wobbles
   between `load-dependent` and `flaky`. Raise `--attempts` when the verdict is borderline.
5. CI gates new problems only. Use the baseline to detect regressions, and stabilize a probabilistic target
   with a representative baseline or `--known-issues`.
6. The skills also produce the console artifacts. scaffold's `to-console.sh` generates the JSON for the
   `:8080` console's three fields (Scenario graph / API templates / Import).

The per-skill detail is in [skills-guide.md](skills-guide.md).

---

> Commands and output are from a real `/tmula-up` session. Run ids (run-22, run-24, run-26 …) and numbers
> change every run, and because the demo's bugs are probabilistic, reproduce and gate results vary a little
> each time.
