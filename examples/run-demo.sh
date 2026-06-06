#!/usr/bin/env bash
# End-to-end tmula demo: starts the sample shop API + the tmula engine, runs an
# experiment from examples/shop, and prints the issues it finds. Requires: go, jq, curl.
#
#   ./examples/run-demo.sh [USERS]
#
set -euo pipefail

USERS="${1:-60}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SAMPLE_ADDR="127.0.0.1:9000"
ENGINE_ADDR="127.0.0.1:8080"
API="http://${ENGINE_ADDR}/api"

cd "$ROOT"

pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do kill "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT

wait_up() { # url
  for _ in $(seq 1 40); do curl -fsS -o /dev/null "$1" 2>/dev/null && return 0; sleep 0.25; done
  echo "timed out waiting for $1" >&2; return 1
}

echo "==> building tmula"
go build -o ./bin/tmula ./cmd/engine

echo "==> starting sample shop API on ${SAMPLE_ADDR}"
SAMPLE_API_ADDR="${SAMPLE_ADDR}" go run ./examples/sample-api >/tmp/sample-api.log 2>&1 &
pids+=($!)
wait_up "http://${SAMPLE_ADDR}/healthz"

echo "==> starting tmula engine on ${ENGINE_ADDR}"
./bin/tmula --role local --addr "${ENGINE_ADDR}" >/tmp/tmula-demo.log 2>&1 &
pids+=($!)
wait_up "http://${ENGINE_ADDR}/healthz"

echo "==> assembling experiment from examples/shop (${USERS} virtual users)"
GRAPH="$(cat examples/shop/graph.json)"
TEMPLATES="$(cat examples/shop/templates.json)"
USERS_JSON="$(jq -nc --argjson n "$USERS" '[range($n) | {id: ("u"+(.|tostring))}]')"
SPEC="$(jq -nc \
  --argjson graph "$GRAPH" --argjson templates "$TEMPLATES" \
  --argjson users "$USERS_JSON" --argjson n "$USERS" \
  --arg base "http://${SAMPLE_ADDR}" '{
    experiment: {name:"shop-demo", targetEnvId:"e", scenarioGraphId:"shop",
      params:{virtualUserCount:$n, deviationRate:0, authStrategy:"pool"}},
    targetEnv: {baseUrl:$base, allowlist:["127.0.0.1"],
      rateCap:{maxRps:20000, maxConcurrency:1000}, envClass:"dev"},
    graph: $graph, templates: $templates,
    start:"browse", maxSteps:10, users:$users, seed:1
  }')"

EXP="$(curl -fsS -X POST "${API}/experiments" -H 'Content-Type: application/json' -d "$SPEC" | jq -r .id)"
RUN="$(curl -fsS -X POST "${API}/experiments/${EXP}/run" | jq -r .runId)"
echo "    experiment=${EXP} run=${RUN}"

echo "==> waiting for the run to finish"
for _ in $(seq 1 60); do
  STATUS="$(curl -fsS "${API}/runs/${RUN}/report" | jq -r .run.status)"
  [ "$STATUS" = "completed" ] && break
  sleep 0.3
done

REPORT="$(curl -fsS "${API}/runs/${RUN}/report")"
echo
echo "================ REPORT ================"
echo "$REPORT" | jq '{requests:.stats.total, errorRatePct:((.stats.errorRate*1000|round)/10),
  p50ms:(.stats.p50|round), p95ms:(.stats.p95|round), statusCounts:.stats.statusCounts}'
echo
echo "---------------- FINDINGS (issues the simulator caught) ----------------"
echo "$REPORT" | jq -r '.findings[] | "  • [\(.severity|ascii_upcase)] \(.category): \(.description)"'
echo
echo "Tip: open http://${ENGINE_ADDR} in a browser to drive it from the UI,"
echo "     or POST ${API}/runs/${RUN}/share to get a read-only viewer link."
