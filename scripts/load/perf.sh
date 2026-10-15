#!/usr/bin/env bash
# Reproducible performance run (make perf): builds the binaries, starts the
# API and a dry-run worker against the compose PostgreSQL and Redis (make
# db-up), runs both k6 scenarios, and collects evidence in .perf/:
#
#   api.txt / queue.txt     k6 summaries (latency percentiles, throughput)
#   cpu.pprof               API CPU profile taken during the steady state
#   top.txt                 hottest functions from that profile
#   queries.txt             pg_stat_statements: queries by total time
#
# DEPLOYMENTS=300000 seeds one large organization first (volume.sh) and runs
# the read load as its members; QUEUE=0 / READS=0 skip a scenario;
# WORKER_CONCURRENCY sets the worker's parallelism (default 2).
#
# Rate limits stay on. The load generator is trusted as a proxy so every
# simulated client has its own IP, as behind an ingress controller.
set -euo pipefail
cd "$(dirname "$0")/../.."

RATE=${RATE:-300}
USERS=${USERS:-60}
VUS=${VUS:-20}
OUT=.perf
mkdir -p "$OUT"
failed=0 # set when a k6 threshold fails; the script exits non-zero at the end

set -a
# shellcheck source=/dev/null
source .env
set +a
# After .env, so its PORT/METRICS_PORT (dev values) don't win.
PORT=${PERF_PORT:-18080}
METRICS=${PERF_METRICS_PORT:-19090}
export PORT METRICS_PORT=$METRICS PPROF_ENABLED=true LOG_LEVEL=${LOG_LEVEL:-info} \
  TRUSTED_PROXY_CIDRS="127.0.0.1/32,::1/128" APP_ENV=development \
  WORKER_BUILDER=dryrun WORKER_DEPLOYER=dryrun WORKER_PORT=18081 KUBE_CONTEXT='' \
  OTEL_EXPORTER_OTLP_ENDPOINT=

echo "==> build"
CGO_ENABLED=0 go build -o bin/api ./cmd/api
CGO_ENABLED=0 go build -o bin/worker ./cmd/worker

echo "==> start API on :$PORT and worker (dry-run)"
bin/api >"$OUT/api.log" 2>&1 &
api=$!
bin/worker >"$OUT/worker.log" 2>&1 &
worker=$!
trap 'kill $api $worker 2>/dev/null || true' EXIT
for _ in $(seq 50); do curl -sf "localhost:$PORT/readyz" >/dev/null && break; sleep 0.2; done
curl -sf "localhost:$PORT/readyz" >/dev/null || { tail -20 "$OUT/api.log"; exit 1; }

psql_q() { docker compose exec -T postgres psql -U clouddeploy -d clouddeploy -qAt -c "$1"; }
psql_q "CREATE EXTENSION IF NOT EXISTS pg_stat_statements" >/dev/null
# DEPLOYMENTS=N: read load against one large organization (N deployments)
# instead of many near-empty ones.
users_file=""
if [[ -n ${DEPLOYMENTS:-} ]]; then
  BASE_URL="http://localhost:$PORT" USERS="$USERS" DEPLOYMENTS="$DEPLOYMENTS" scripts/load/volume.sh
  users_file="$OUT/volume-users.json"
fi
psql_q "SELECT pg_stat_statements_reset()" >/dev/null

if [[ ${READS:-1} == 1 ]]; then
echo "==> API read load: $RATE req/s over $USERS users (20s ramp + 60s steady)"
# CPU profile over 30s of the steady state (starts after setup + ramp).
[[ -n $users_file ]] && PROFILE_DELAY=${PROFILE_DELAY:-25}
( sleep "${PROFILE_DELAY:-35}"; curl -s "localhost:$METRICS/debug/pprof/profile?seconds=30" -o "$OUT/cpu.pprof" ) &
prof=$!
k6 run --quiet -e BASE_URL="http://localhost:$PORT" -e RATE="$RATE" -e USERS="$USERS" \
  ${users_file:+-e USERS_FILE="$PWD/$users_file"} scripts/load/api.js \
  --summary-export "$OUT/api.json" 2>&1 | tee "$OUT/api.txt" || failed=1
wait $prof || true
go tool pprof -top -nodecount=25 bin/api "$OUT/cpu.pprof" >"$OUT/top.txt" 2>/dev/null || true

psql_q "SELECT round(total_exec_time::numeric,1) AS total_ms, calls, round(mean_exec_time::numeric,3) AS mean_ms,
          left(regexp_replace(query, '\s+', ' ', 'g'), 140) AS query
        FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = 'clouddeploy')
        ORDER BY total_exec_time DESC LIMIT 12" >"$OUT/queries.txt"

fi

if [[ ${QUEUE:-1} == 0 ]]; then echo "==> results in $OUT/"; exit "$failed"; fi
echo "==> deployment pipeline: $VUS concurrent deployments for 60s"
k6 run --quiet -e BASE_URL="http://localhost:$PORT" -e VUS="$VUS" scripts/load/queue.js \
  --summary-export "$OUT/queue.json" 2>&1 | tee "$OUT/queue.txt" || failed=1

echo "==> results in $OUT/ (api.txt, queue.txt, top.txt, queries.txt)"
[[ $failed == 0 ]] || echo "==> a k6 threshold failed"
exit "$failed"
