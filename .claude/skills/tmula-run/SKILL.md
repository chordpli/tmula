---
name: tmula-run
description: Run a tmula scenario as a load test, safely. Enforces a non-production safety gate, smoke-tests with one user, then executes the load (closed or open arrival model) and reads the findings honestly. Use when you have a runnable json/scenario.json and want to actually send traffic / stress-test / load-test the target. This is the step that sends real HTTP requests.
argument-hint: "[scenario path] (defaults to json/scenario.json) — target must be a confirmed non-prod host"
---

> **Required input — ask, never invent.** This skill needs a runnable scenario and a user-confirmed target.
> If no scenario is given and `json/scenario.json` doesn't exist, **STOP and ask** (or run tmula-scaffold +
> tmula-enrich first). Never invent a target/host, and never run until the Safety Gate below is cleared.

# tmula-run

Execute a tmula `json/scenario.json` against its target and report findings. **This is the step that sends real
HTTP traffic**, so it enforces a safety gate first, smokes with one user, then runs the real load.

## Inputs (Source options — works standalone)

- A scenario file path, else `json/scenario.json` in the working directory, else ask. Works on any runnable
  scenario (from tmula-enrich, hand-written, or pasted). If the scenario still has literal `{path-params}`,
  no auth on protected endpoints, or destructive ops, send the user to **tmula-enrich** first.

## Safety Gate — STOP and clear all of these before any run

1. **Target is non-production.** Read `target` from the scenario and get the user's explicit confirmation
   that it is a sandbox/staging host they own or are allowed to load. **Never run against production.**
2. **No unintended destructive ops.** Scan the flow for `DELETE` / mutating `POST`/`PUT`/`PATCH`. If any
   are present, name them and get explicit opt-in; otherwise stop and route to tmula-enrich to filter them.
3. **`--users 1` is still real traffic.** It lowers volume, it does not make an unconfirmed host safe.

tmula keeps `envClass=dev`, `allow` = target host (fail-closed), `rateCap`={maxRps:10000,maxConcurrency:1000}
by default — keep them; never widen `allow` blindly.

**Bootstrap-signup runs** (`auth.strategy: bootstrap-signup`) create and delete **real accounts** in the
target. They must never run against production. Teardown runs even on kill/timeout (best-effort). If the
signup flow declares no teardown, pass `--keep-accounts` to opt out — without it the run is rejected.

A deterministic guard hook (`.claude/hooks/tmula-guard`) backs this up: it **blocks** a `tmula run` whose
target is not loopback/private, or whose scenario file is missing. If a run is blocked for a host you're
genuinely allowed to load, confirm with the user, then `export TMULA_ALLOW_TARGET="host"` (or add it to a
`.tmula-allow` file) and re-run — don't try to bypass the guard any other way.

## Process

0. **Binary**: from the repo root, `go build -o ./bin/tmula ./server/cmd/tmula` (if not already built).

1. **Clear the Safety Gate** above. Do not proceed until confirmed.

2. **Dry-run (1 user).** The dry-run is a minimal-load pass that proves the scenario is well-formed and the
   target responds — tmula has no zero-traffic validation, so the dry-run is the smallest real run:

   ```bash
   ./bin/tmula run json/scenario.json --users 1 --timeout 30s --fail-on-findings
   ```

   **Exit code alone is NOT proof of success.** A plain run **exits 0 even at 100% errors** — pass
   `--fail-on-findings` (any finding) or `--fail-on-severity warning|critical` (severity-filtered) to make
   the exit code meaningful (exit 2 when a matching finding fires). Always READ the printed
   `requests=` / `errors=` / `status:` / `Findings` summary, and show it to the user. High errors here are
   often the *target's* own faults — that's tmula doing its job, not a scenario bug. Confirm a suspicious
   status with plain `curl` if unsure. A clean 1-user dry-run can still hide load-only failures — that's
   expected; raise the load in step 3.

3. **Real load** (only after a sane dry-run). **Ask the user for the load before running - never assume a
   number.** Ask which model and how much:
   - **closed model**: how many concurrent virtual users (`--users N`)?
   - **open model**: arrivals per second and for how long (`--open R --for S`), and an optional ramp
     (`--ramp-to R`)?

   If the user has no preference, propose a sensible starting point (e.g. `--users 50`) and confirm before
   sending it. Then run:

   ```bash
   ./bin/tmula run json/scenario.json --users 50                 # closed: 50 concurrent virtual users
   ./bin/tmula run json/scenario.json --open 278 --for 3600      # open: 278 arrivals/sec for 3600s
   ./bin/tmula run json/scenario.json --open 50 --ramp-to 500 --for 600   # open with a ramp
   ```

   Other flags: `--target <url>` (override), `--seed N`, `--timeout`, `--fail-on-severity warning|critical`,
   `--auth-source file:./pool.csv|env:VAR` (+ `--auth-format csv|jsonl|tokens`) to attach a credential pool without editing the scenario,
   `--json` (raw report — save it as `json/report.json`; that's what tmula-triage consumes as `--baseline-file`):
   `./bin/tmula run json/scenario.json --json > json/report.json`.

   `--keep-accounts`: bootstrap-signup only. Skips teardown and leaves provisioned accounts in place. Required
   when the `auth.signup` block declares no teardown flow.

   **If you intend to triage by `reproduce` afterward, run on a live engine:** start the engine with
   `./bin/tmula` (bare, :8080), then `./bin/tmula run json/scenario.json --engine http://localhost:8080 ...`.
   A plain in-process run is **not retained**, so `tmula reproduce` can't replay it.

4. **Report findings honestly.** Summarize `requests / errors / p50·p95·p99 / status counts / findings`
   (category + which endpoint). Don't claim "passed" from an exit code — quote the numbers.

5. **Hand off.** If findings appeared: *"Run **tmula-triage** to reproduce a finding (functional vs
   load-dependent) and set up a baseline gate."*

## Run it in the web console (the web environment)

To run server-side and watch it live in the browser, point the run at a live engine instead of running
in-process. `tmula run --engine` submits the scenario (`POST /api/experiments` → `/run`), executes it on
the engine, and **retains** it — so the console can attach to it by id:

```bash
make web                                  # one-time: build + embed the React console (else the UI is a placeholder)
./bin/tmula --addr :8080                  # start the engine + console (bare invocation; NO `serve` subcommand)
./bin/tmula run json/scenario.json --engine http://localhost:8080 --users 1 --timeout 30s   # dry-run, server-side
# → prints a run id (e.g. run-2); open it live:
#   http://localhost:8080/?run=<run-id>
```

Notes:
- The console renders the live traffic-flow map + findings; without `make web` the engine still runs and the
  run is attachable, but `/` serves a placeholder page.
- **A credential pool cannot cross the wire to a remote `--engine`** (the secret is never sent) — an authed
  scenario must run in-process (`tmula run json/scenario.json`, no `--engine`). Auth-free scenarios run fine on
  the engine. The same non-prod safety gate above applies to engine runs.
- A server-side run is also what **tmula-triage** needs for `reproduce` (in-process runs aren't retained).

## Exit codes

`0` ok · `1` run error · `2` findings (only with `--fail-on-findings`) · `3` new findings vs `--baseline`.

## Iron Laws

- **Never run against a host the user hasn't confirmed is non-production.**
- **Never run a flow containing DELETE / mutating ops** without explicit opt-in on a confirmed sandbox.
- **Never run `bootstrap-signup` against production** — it provisions and tears down real accounts.
- **Exit 0 ≠ success.** Read and show the real `errors=`/`findings=` output before reporting an outcome.
- Keep the fail-closed defaults (`envClass=dev`, `allow`=target host). Don't widen `allow` to silence a
  block unless the extra host is genuinely intended.

## Failure modes

- All requests 404/400 with `{param}` in the path → scenario wasn't enriched; route to tmula-enrich.
- Protected endpoints 401/403 → missing `{{.token}}` header or unfilled placeholder; tmula-enrich.
- Run blocked reaching a host → `allow` fail-closed; add the host only if intended.
- Timeout on a slow/public host → raise `--timeout` (30s+ for remote), or reduce `--users`.
- Smoke shows 5xx from the target itself → real finding; reproduce/confirm before assuming a scenario bug.
- `tmula run` with no scenario arg → it expects a file (or single-endpoint `--get`/`--post` + `--target`);
  pass `json/scenario.json`.

## Example

```bash
# after confirming the target is a sandbox:
./bin/tmula run json/scenario.json --users 1 --timeout 30s --fail-on-findings   # smoke — READ the summary
./bin/tmula run json/scenario.json --users 50                                   # real load
# → requests=… errors=… status:{…} Findings(…). If findings: tmula-triage.
```
