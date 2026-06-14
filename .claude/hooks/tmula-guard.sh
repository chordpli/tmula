#!/usr/bin/env bash
# tmula-guard — a PreToolUse(Bash) guard that backs up the tmula skills' safety
# gate deterministically. It blocks a `tmula run` (real load traffic) when:
#   1. the run targets a host that is NOT loopback/private (i.e. possibly prod),
#      unless the host is explicitly allowed, or
#   2. the named scenario file does not exist (missing required input).
# Everything else passes untouched. The skills already ask/confirm; this is the
# backstop for when something slips through.
#
# Opt in to load an external host you ARE allowed to test:
#   - export TMULA_ALLOW_TARGET="staging.example.com"   (comma/space list, or "all")
#   - or add the host (one per line) to a .tmula-allow file in the repo root.
#
# Wired from .claude/settings.json. Reads the PreToolUse JSON on stdin; the
# python core exits 2 to block the tool call and shows its message to the agent.
set -u
payload="$(cat)"

# Fast path: only tmula commands are worth parsing — skip everything else cheaply.
case "$payload" in
  *tmula*) ;;
  *) exit 0 ;;
esac

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf '%s' "$payload" | python3 "$dir/tmula-guard.py" "${CLAUDE_PROJECT_DIR:-$PWD}"
