#!/usr/bin/env bash
# Seed a production-sized organization for load tests: USERS members (real
# accounts, registered through the API) sharing one organization with 50
# projects, 100 environments, DEPLOYMENTS deployments spread over 30 days,
# and an audit trail. Writes the members' tokens to .perf/volume-users.json
# for scripts/load/api.js (USERS_FILE).
#
# Usage (API running on BASE_URL, compose PostgreSQL up):
#   DEPLOYMENTS=300000 USERS=60 scripts/load/volume.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

BASE=${BASE_URL:-http://localhost:18080}
USERS=${USERS:-60}
DEPLOYMENTS=${DEPLOYMENTS:-300000}
RUN=$(date +%s)
OUT=.perf
mkdir -p "$OUT"
psql_q() { docker compose exec -T postgres psql -U clouddeploy -d clouddeploy -v ON_ERROR_STOP=1 -qAt "$@"; }

echo "==> registering $USERS users"
tokens=()
user_ids=()
for i in $(seq 1 "$USERS"); do
  ip="10.79.$(( i >> 8 & 255 )).$(( i & 255 ))"
  reg=$(curl -sf -X POST "$BASE/api/v1/auth/register" -H 'Content-Type: application/json' -H "X-Forwarded-For: $ip" \
    -d "{\"email\":\"vol-$RUN-$i@example.com\",\"name\":\"Volume $i\",\"password\":\"volume-password-$RUN\",\"organization_name\":\"Volume $RUN $i\"}")
  tokens+=("$(jq -r .access_token <<<"$reg")")
  user_ids+=("$(jq -r .user.id <<<"$reg")")
done
org=$(psql_q -c "SELECT organization_id FROM organization_members WHERE user_id = '${user_ids[0]}'")

echo "==> generating volume in organization $org"
ids=$(printf "'%s'," "${user_ids[@]:1}"); ids=${ids%,}
psql_q <<SQL
BEGIN;
-- Everyone joins the first user's organization (they keep their own too).
INSERT INTO organization_members (organization_id, user_id, role)
SELECT '$org', id, 'developer' FROM users WHERE id IN ($ids);

CREATE TEMP TABLE vol_projects AS
WITH p AS (
  INSERT INTO projects (organization_id, name, repository_url)
  SELECT '$org', 'vol-$RUN-' || g, 'https://github.com/example/volume' FROM generate_series(1, 50) g
  RETURNING id, name)
SELECT * FROM p;

CREATE TEMP TABLE vol_envs AS
WITH e AS (
  INSERT INTO environments (project_id, name, namespace)
  SELECT p.id, env, left(p.name || '-' || env, 63)
  FROM vol_projects p CROSS JOIN (VALUES ('staging'), ('production')) v(env)
  RETURNING id, project_id)
SELECT row_number() OVER () AS n, * FROM e;

-- Finished deployments over the last 30 days: ~85% RUNNING, 12% FAILED,
-- 3% CANCELLED, each with a realistic duration.
INSERT INTO deployments (project_id, environment_id, commit_sha, status, image, created_at, started_at, completed_at)
SELECT e.project_id, e.id, substr(md5(g::text), 1, 12),
       CASE WHEN g % 100 < 85 THEN 'RUNNING' WHEN g % 100 < 97 THEN 'FAILED' ELSE 'CANCELLED' END,
       'example/volume:' || substr(md5(g::text), 1, 12),
       t, t + interval '2 seconds', t + interval '2 seconds' + (g % 300) * interval '1 second'
FROM generate_series(1, $DEPLOYMENTS) g
JOIN vol_envs e ON e.n = 1 + g % (SELECT count(*) FROM vol_envs)
CROSS JOIN LATERAL (SELECT now() - (g::float / $DEPLOYMENTS) * interval '30 days' AS t) ts;

INSERT INTO audit_logs (organization_id, actor_id, action, resource_type, resource_id, created_at)
SELECT '$org', '${user_ids[0]}', 'deployment.create', 'deployment', gen_random_uuid()::text,
       now() - (g::float / 100000) * interval '30 days'
FROM generate_series(1, 100000) g;
COMMIT;
ANALYZE;
SQL
psql_q -c "SELECT 'deployments in org: ' || count(*) FROM deployments d JOIN projects p ON p.id = d.project_id WHERE p.organization_id = '$org'"

echo "==> writing $OUT/volume-users.json"
proj=$(psql_q -c "SELECT id FROM projects WHERE organization_id = '$org' AND name LIKE 'vol-%' ORDER BY name LIMIT 1")
{
  echo '{"users":['
  for i in "${!tokens[@]}"; do
    n=$((i + 1))
    [[ $i -gt 0 ]] && echo ','
    printf '{"token":"%s","ip":"10.79.%d.%d","org":"%s","project":"%s"}' \
      "${tokens[$i]}" $(( n >> 8 & 255 )) $(( n & 255 )) "$org" "$proj"
  done
  echo ']}'
} >"$OUT/volume-users.json"
