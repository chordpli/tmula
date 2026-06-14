#!/usr/bin/env bash
# to-console — turn an API source (OpenAPI/Swagger, HAR, or access log) into the
# exact JSON the tmula web console's manual-edit fields expect, by POSTing it to a
# running engine's /api/import (the same conversion the console's "Import" does).
# Writes, under json/:
#   json/graph.json      → paste into the console's "Scenario graph" field
#   json/templates.json  → paste into "API templates"
# and prints the Start node + Max steps (the console's other two fields).
#
#   to-console.sh <engine-url> <source-file> [format]
#     format: auto (default) | openapi | har | accesslog
#
# Needs a running engine: start one with `./bin/tmula --addr :8080`.
set -euo pipefail

ENGINE="${1:?usage: to-console.sh <engine-url> <source-file> [format]}"
SRC="${2:?source file required}"
FMT="${3:-auto}"

command -v curl >/dev/null || { echo "to-console: curl not found" >&2; exit 1; }
command -v python3 >/dev/null || { echo "to-console: python3 not found" >&2; exit 1; }
[ -f "$SRC" ] || { echo "to-console: source file not found: $SRC" >&2; exit 1; }
mkdir -p json

resp="$(mktemp -t tmula-import-XXXXXX)"
trap 'rm -f "$resp"' EXIT
if ! curl -fsS -X POST "${ENGINE%/}/api/import?format=${FMT}" --data-binary @"$SRC" -o "$resp" 2>/dev/null; then
  echo "to-console: import request failed — is the engine running at $ENGINE?" >&2
  echo "  start one with:  ./bin/tmula --addr :8080" >&2
  exit 1
fi

# python reads the response FROM THE FILE (argv), so the heredoc owns stdin and
# there is no stdin conflict.
python3 - "$resp" <<'PY'
import sys, json
try:
    r = json.load(open(sys.argv[1]))
except Exception as e:
    sys.stderr.write(f"to-console: engine did not return JSON ({e})\n"); sys.exit(1)
if "graph" not in r or "templates" not in r:
    sys.stderr.write("to-console: import response missing graph/templates "
                     "(the source may not be a usable OpenAPI/HAR/access log)\n"); sys.exit(1)
for name, key in (("json/graph.json", "graph"), ("json/templates.json", "templates")):
    with open(name, "w") as f:
        json.dump(r[key], f, indent=2); f.write("\n")
print(f"wrote json/graph.json + json/templates.json  (Start node: {r.get('start')!r}  ·  Max steps: {r.get('maxSteps')})")
st = r.get("stats")
if st:
    extra = f", {st['format']} format" if st.get("format") else ""
    print(f"  import coverage: {st.get('requests','?')} requests, {st.get('skipped',0)} skipped{extra}")
PY
