# tmula Claude Code skills

A suite of [Claude Code](https://docs.claude.com/en/docs/claude-code) skills that take you from "here's an
API" to "here's what breaks under load" with tmula. They live in `.claude/skills/` and are auto-discovered
when you open this repo in Claude Code - invoke one with its slash name (e.g. `/tmula-up`) or just describe
what you want ("load-test this API from its swagger").

> Want the detailed walkthrough - each skill's process and real example output? See the full guide:
> [English](skills-guide.md) · [한국어](skills-guide.ko.md).
>
> First time? Walk the whole thing end-to-end against the bundled demo API:
> [English](skills-tutorial.md) · [한국어](skills-tutorial.ko.md).

## The suite

Four **atoms** (each works standalone, no dependency on the others) and one **orchestrator** that chains
them:

```
  source                json/scenario.json                 run                findings
 (spec/HAR/log) ─▶ tmula-scaffold ─▶ tmula-enrich ─▶ tmula-run ─▶ tmula-triage
                          │               │              │             │
                          └───────────────┴──── tmula-up (orchestrates all four) ────┘
```

| Skill | Does | Standalone use |
|---|---|---|
| **tmula-scaffold** | Spec/HAR/access-log → raw `json/scenario.json` + target | "turn my swagger into a tmula scenario" |
| **tmula-enrich** | Raw scenario → runnable & safe (path params, auth, drop destructive ops) | "make my scenario runnable" |
| **tmula-run** | Scenario → load test, behind a non-prod safety gate | "load-test this safely" |
| **tmula-triage** | Finished run → reproduce (functional vs load-dependent), baseline gate, CI | "is this finding real?" |
| **tmula-up** | Orchestrates scaffold → enrich → run → triage end-to-end | "do the whole thing from this URL" |

## How they stay independent

Atoms pass **artifacts**, not control. The contract:

Generated JSON lives under a `json/` folder (gitignored):

- `json/scenario.json` - the compact scenario for the CLI (scaffold writes it; enrich rewrites it; run/triage consume it).
- `json/report.json` - a `tmula run --json` capture (triage's baseline gate consumes it).
- `json/graph.json` + `json/templates.json` + `json/source.<ext>` - the **web console** edit-field JSON (see below).
- `./bin/tmula` - built once with `go build -o ./bin/tmula ./server/cmd/tmula`.

Each atom takes its input from an explicit path, else the conventional file, else it asks - so none of them
requires another to have run first. `tmula-up` is pure convenience; you never *have* to use it.

### For the web console (`:8080`)

The console has three JSON inputs; the skills produce a file for each so you can paste/upload directly:

| Console field | File | Produced by |
|---|---|---|
| **Scenario graph** | `json/graph.json` | tmula-scaffold (via `to-console.sh` → `/api/import` on a running engine) |
| **API templates** | `json/templates.json` | same |
| **Import from OpenAPI / HAR / access log** | `json/source.<ext>` | tmula-scaffold (the raw fetched source) |

`graph.json` / `templates.json` are the **import baseline** (what the console's *Import* produces from the
source); edit them in the console's fields the way tmula-enrich edits the CLI `json/scenario.json`. The
console's *Start node* / *Max steps* fields are printed by `to-console.sh`.

## Safety model

Running a scenario sends **real HTTP traffic**. The hard gate lives in `tmula-run` (and is restated by
`tmula-up`): the target must be confirmed non-production, the flow is **read-only by default**, and
DELETE/mutating ops require explicit opt-in. Fetched specs/logs are treated as **untrusted data**. tmula's
own defaults back this up: `envClass=dev`, `allow` = target host (fail-closed).

A **deterministic guard hook** (`.claude/hooks/tmula-guard`, wired in `.claude/settings.json`) backs the
gate up at the tool level: it **blocks any `tmula run`** whose target is not loopback/private (i.e. possibly
production), and any run whose scenario file is missing. To load a real sandbox/staging host on purpose, opt
in with `export TMULA_ALLOW_TARGET="host"` or a `.tmula-allow` file. (Hooks load at session start - reload
the session after first checkout.)

## Quick start

```bash
# whole pipeline from a Swagger URL:
/tmula-up   https://api.example.com/openapi.json

# or one atom at a time:
/tmula-scaffold  https://api.example.com/openapi.json   # → json/scenario.json
/tmula-enrich                                            # → runnable, safe json/scenario.json
/tmula-run                                               # → load test (after the safety gate)
/tmula-triage                                            # → reproduce / baseline / CI
```
