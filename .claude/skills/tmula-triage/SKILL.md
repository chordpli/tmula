---
name: tmula-triage
description: Triage a finished tmula run — reproduce a finding in isolation to tell a functional bug from a load-dependent one, gate the run against a baseline, and wire it into CI. Use after a tmula run produced findings and you want to know which are real, whether anything regressed, or how to make load testing a CI gate. Reproduce sends a small amount of real traffic to replay the failing session.
argument-hint: <engine URL + run id (to reproduce) | json/report.json (to baseline-gate)>
---

> **Required input — ask, never invent.** This skill needs something to triage: a live engine URL + run id
> (for reproduce), or a saved `--json` report (for a baseline gate). If none is given, **STOP and ask** the
> user which they have. Never guess a run id, engine URL, or finding selector.

# tmula-triage

After a tmula run finds something, decide what it means: is a finding a **functional** bug or only
**load-dependent**? Did anything **regress** vs a known-good baseline? How do you make this a **CI gate**?

## Inputs (Source options — works standalone)

- A finding to reproduce: a finished run held by a live engine (`--engine <url>` + `--run <id>` + a finding
  selector), OR
- A saved report for a baseline gate: a `tmula run --json` output file (e.g. `json/report.json`), OR
- Just a scenario you want to gate going forward.
- If none is clear, ask whether the goal is **reproduce**, **baseline gate**, or **CI wiring**.

## A. Reproduce a finding (functional vs load-dependent)

**Safety first:** `reproduce` sends real traffic (a small replay) to the same target the run used. Before
running it, confirm that target is the **non-production** sandbox the run already hit — restate tmula-run's
gate and refuse known production hosts. Reproduce never re-verifies the target on its own.

`reproduce` replays one evidence session **alone, with no concurrent load**, and classifies the root cause.
It must talk to the **engine that still holds the run in memory** — so the run has to have happened against
a live engine, not a one-shot in-process `tmula run`.

```bash
./bin/tmula                                              # start the engine + UI on :8080 (bare invocation)
./bin/tmula run json/scenario.json --engine http://localhost:8080 --users 30   # run ON the engine (it retains the run)
./bin/tmula reproduce --engine http://localhost:8080 --run <run-id> --finding contract/checkout --attempts 5
```

Get a valid selector from the run's findings — each is printed as `category/evidenceRef` in the run output,
the web report, or `GET /api/runs/<id>/report` — or use its 1-based index in that list. The selector is
`category/evidenceRef` (e.g. `contract/checkout`) or the index. Verdict:
- **functional** — failed every attempt under no load → likely a real bug, reproducible standalone.
- **load-dependent** — never failed alone → concurrency/saturation; chase resource limits, not the handler.
- **flaky** — failed some attempts → probabilistic; raise `--attempts` to sharpen the read.

The verdict is a **signal, not a proof**: reproduce replays the same seed + walk, but not the original
timing/concurrency or target state. tmula prints that caveat with every result — keep it in your report.

## B. Baseline gate (did anything regress?)

Compare a run's findings against a known-good baseline; fail only on **new** findings.

```bash
# one-time: capture a known-good baseline report
./bin/tmula run json/scenario.json --json > json/report.json
# later runs: gate against it — regression gate, exit 3 ONLY on new findings vs the baseline
./bin/tmula run json/scenario.json --baseline-file json/report.json
# or against a baseline run id held by a live engine:
./bin/tmula run json/scenario.json --engine http://localhost:8080 --baseline <baseline-run-id>
```

> **Do NOT add `--fail-on-findings` here.** The exit ladder checks `--fail-on-findings`/`--fail-on-severity`
> (exit 2, the *absolute* gate — fails on ANY finding) **before** the baseline gate (exit 3, *new only*). So
> `--baseline-file … --fail-on-findings` exits 2 on findings already in the baseline — it defeats the
> "new only" comparison. Use the baseline flags **alone** for a regression gate; use `--fail-on-findings`
> **alone** when you want to fail on any finding regardless of baseline.

Constraints: `--baseline <run-id>` requires `--engine` (it fetches the baseline from the live engine);
`--baseline-file` reads a saved report instead. They are **mutually exclusive** — never pass both.

Suppress issues you already know about, with a mandatory expiry so "ignore" can't become permanent:

```bash
./bin/tmula run json/scenario.json --baseline-file json/report.json --known-issues known-issues.yaml
```

`known-issues.yaml` entries carry an `expires: YYYY-MM-DD`; past that date the finding re-surfaces.
(`--known-issues` only affects the baseline gate, so it requires `--baseline`/`--baseline-file`.)

## C. CI wiring

Exit codes make a clean gate: `0` ok · `1` run error · `2` any finding (with `--fail-on-findings` or
`--fail-on-severity`) · `3` new findings vs `--baseline`. Pick **one** gate:

```bash
# regression gate — fail only on NEW findings vs a known-good baseline (exit 3):
./bin/tmula run json/scenario.json --baseline-file json/report.json --known-issues known-issues.yaml \
  --summary "$GITHUB_STEP_SUMMARY"

# OR absolute gate — fail on any finding at/above a severity (exit 2):
./bin/tmula run json/scenario.json --fail-on-severity critical --summary "$GITHUB_STEP_SUMMARY"
```

`--fail-on-severity warning|critical` is its own gate (exit 2) and works with or without
`--fail-on-findings`. The repo also ships a composite GitHub Action (`action.yml`) that runs the gate and
upserts the result as a PR comment — point the user to it for a PR-native gate.

## Iron Laws

- **reproduce sends real traffic** (a small replay) — confirm the run's target is the non-production sandbox
  before running it; never reproduce against production.
- **Don't combine the baseline gate with `--fail-on-findings`/`--fail-on-severity`** — the absolute gate
  (exit 2) preempts the regression gate (exit 3), so the baseline comparison is silently ignored.
- **Report the verdict as a signal, not proof** — always include tmula's timing/concurrency caveat.
- **A baseline must be a known-good run.** Don't bless a baseline that already contains the failure you're
  trying to gate against — you'd suppress the very regression you care about.
- **Every `known-issues` entry needs an `expires`** — no permanent silencing.

## Failure modes

- `reproduce` can't find the run → the run wasn't done against the live `--engine` (in-process runs aren't
  retained); re-run with `--engine <url>` then reproduce.
- Finding selector not found → use `category/evidenceRef` exactly as printed, or the 1-based index.
- Baseline shows everything as "new" → wrong/empty baseline file; capture a fresh `--json` baseline.
- Gate never fails → missing `--fail-on-findings`/`--baseline`; plain runs exit 0 regardless of findings.
- Baseline gate fails on a known issue → you added `--fail-on-findings` alongside `--baseline-file`; drop it
  (the absolute gate preempts the regression gate), or suppress the issue in `--known-issues`.

## Example

```bash
./bin/tmula run json/scenario.json --engine http://localhost:8080 --users 50      # run-2 on the engine
./bin/tmula reproduce --engine http://localhost:8080 --run run-2 --finding contract/checkout --attempts 5
# Verdict: load-dependent (0/5 reproduced under no load) → chase concurrency/saturation, not the handler.
```
