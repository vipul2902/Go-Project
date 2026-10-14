#!/usr/bin/env bash
# End-to-end release check: install CloudDeploy into a Kubernetes cluster
# from the given images and chart, then drive the real API through a full
# deployment.
#
# The chain under test is: web (nginx) -> API -> PostgreSQL queue -> worker
# -> Kubernetes deployer -> tenant pods Ready -> environment health.
# Builds are the dry-run builder (tenant builds need ACR Tasks or a Docker
# socket), which names the image "<project>:<commit>"; this script prebuilds
# a tiny image under that name, so the deploy step is real.
#
# Requirements: kubectl, helm, docker, curl, jq, openssl; a cluster that
# shares the local Docker image store (k3s --docker in CI, Colima locally).
# All credentials are generated per run and never printed. The script
# removes everything it created when it exits (E2E_KEEP=1 keeps it).
#
#   KUBE_CONTEXT=colima IMAGE_REPO=clouddeploy WEB_IMAGE_REPO=clouddeploy-web \
#     IMAGE_TAG=dev-abc scripts/e2e.sh
set -euo pipefail

: "${KUBE_CONTEXT:?KUBE_CONTEXT is required}"
: "${IMAGE_REPO:?}" "${WEB_IMAGE_REPO:?}" "${IMAGE_TAG:?}"
CHART=${CHART:-deploy/helm/clouddeploy}      # path or oci:// reference
CHART_VERSION=${CHART_VERSION:-}             # for oci:// charts
# Its own release name and namespaces, so it can run beside a dev install
# (ClusterRoles are cluster-wide and named after the release).
RELEASE=${RELEASE:-clouddeploy-e2e}
NS=${NS:-clouddeploy-e2e}
DEPS=${DEPS:-clouddeploy-e2e-deps}
PORT=${E2E_PORT:-18090}
TENANT_PROJECT=smoke
TENANT_SHA=e2e0001

k() { kubectl --context "$KUBE_CONTEXT" "$@"; }
log() { printf '\n==> %s\n' "$*"; }
fail() { printf '\nE2E FAILED: %s\n' "$*" >&2; diagnostics; exit 1; }
diagnostics() {
  set +e
  echo "---- pods"; k get pods -A -o wide
  echo "---- events (last 40)"; k get events -A --sort-by=.lastTimestamp | tail -40
  for c in api worker; do echo "---- $c logs"; k -n "$NS" logs "deploy/$RELEASE-$c" --tail=60 --all-containers; done
}

# Everything this script creates, and nothing else: its release, its two
# namespaces and the tenant namespace the worker creates for the smoke app.
cleanup() {
  helm uninstall "$RELEASE" --kube-context "$KUBE_CONTEXT" -n "$NS" >/dev/null 2>&1 || true
  k delete namespace "$NS" "$DEPS" "$TENANT_PROJECT-staging" --ignore-not-found --wait=true >/dev/null 2>&1 || true
}
log "removing any previous e2e install"
cleanup

# ---- 1. dependencies (in-cluster PostgreSQL and Redis, as in local dev) ----
log "dependencies"
pg_password=$(openssl rand -hex 24)
jwt_secret=$(openssl rand -base64 48 | tr -d '\n')
k create namespace "$DEPS" --dry-run=client -o yaml | k apply -f - >/dev/null
printf 'password=%s\n' "$pg_password" | k -n "$DEPS" create secret generic postgres-credentials \
  --from-env-file=/dev/stdin --dry-run=client -o yaml | k apply -f - >/dev/null
# The manifests name the dev namespace; drop it so they land in $DEPS.
for f in deploy/kubernetes/dev-deps/*.yaml; do cat "$f"; echo "---"; done \
  | sed '/^  namespace: /d' | k -n "$DEPS" apply -f - >/dev/null
k -n "$DEPS" rollout status statefulset/postgres --timeout=180s
k -n "$DEPS" rollout status deployment/redis --timeout=120s

# ---- 2. CloudDeploy namespace and secret (restricted Pod Security) ----------
log "namespace and secret"
k create namespace "$NS" --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace "$NS" --overwrite pod-security.kubernetes.io/enforce=restricted >/dev/null
{ printf 'DATABASE_URL=postgres://clouddeploy:%s@postgres.%s.svc.cluster.local:5432/clouddeploy?sslmode=disable\n' "$pg_password" "$DEPS"
  printf 'REDIS_URL=redis://redis.%s.svc.cluster.local:6379/0\n' "$DEPS"
  printf 'JWT_SECRET=%s\n' "$jwt_secret"; } \
  | k -n "$NS" create secret generic e2e-secrets --from-env-file=/dev/stdin \
      --dry-run=client -o yaml | k apply -f - >/dev/null

# ---- 3. tenant image the dry-run builder will name -------------------------
log "tenant image ${TENANT_PROJECT}:${TENANT_SHA}"
docker build -q -t "${TENANT_PROJECT}:${TENANT_SHA}" - >/dev/null <<'EOF'
FROM busybox:1.37
RUN mkdir -p /www && echo ok > /www/healthz && echo hello > /www/index.html
USER 65532:65532
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
EOF

# ---- 4. install the release -------------------------------------------------
log "helm install ($CHART ${CHART_VERSION})"
helm upgrade --install "$RELEASE" "$CHART" ${CHART_VERSION:+--version "$CHART_VERSION"} \
  --kube-context "$KUBE_CONTEXT" -n "$NS" \
  -f deploy/helm/clouddeploy/values-staging.yaml \
  --set image.repository="$IMAGE_REPO" --set image.tag="$IMAGE_TAG" \
  --set web.image.repository="$WEB_IMAGE_REPO" --set web.image.tag="$IMAGE_TAG" \
  --set image.pullPolicy=IfNotPresent --set existingSecret=e2e-secrets \
  --set api.replicas=1 --set api.pdb.enabled=false \
  --set worker.builder=dryrun --set worker.deployer=kubernetes \
  --set ingress.enabled=false \
  --wait --timeout 6m || fail "helm install"

# ---- 5. drive the API through the web front end -----------------------------
log "port-forward web -> localhost:$PORT"
k -n "$NS" port-forward "svc/$RELEASE-web" "$PORT:80" >/dev/null 2>&1 &
pf=$!
# Clean up on exit unless E2E_KEEP=1 (to inspect a failed run).
trap 'kill $pf 2>/dev/null || true; [[ ${E2E_KEEP:-} == 1 ]] || cleanup' EXIT
for _ in $(seq 30); do curl -sf "localhost:$PORT/" >/dev/null && break; sleep 1; done

base="http://localhost:$PORT/api/v1"
api() { # method path [json] -> body; fails on non-2xx
  local out code
  out=$(curl -s -w '\n%{http_code}' -X "$1" "$base$2" -H "Authorization: Bearer ${token:-}" \
        -H 'Content-Type: application/json' ${3:+-d "$3"})
  code=${out##*$'\n'}; out=${out%$'\n'*}
  [[ $code == 2* ]] || fail "$1 $2 -> HTTP $code: $out"
  printf '%s' "$out"
}

log "dashboard"
curl -sf "localhost:$PORT/" | grep -q '<div id="root">' || fail "dashboard HTML not served"

log "register, create project and environment"
password=$(openssl rand -hex 16)
reg=$(api POST /auth/register "{\"email\":\"e2e@example.com\",\"name\":\"E2E\",\"password\":\"$password\",\"organization_name\":\"E2E Org\"}")
token=$(jq -r .access_token <<<"$reg")
org=$(api GET /auth/me | jq -r '.memberships[0].organization_id')
[[ $token != null && $org != null ]] || fail "register did not return a token and organization"
project=$(api POST /projects "{\"organization_id\":\"$org\",\"name\":\"$TENANT_PROJECT\",\"repository_url\":\"https://github.com/example/smoke\"}" | jq -r .id)
api POST "/projects/$project/environments" '{"name":"staging","health_check_path":"/healthz","replicas":1}' >/dev/null

log "deploy and wait for the worker"
dep=$(api POST /deployments "{\"project_id\":\"$project\",\"environment\":\"staging\",\"commit_sha\":\"$TENANT_SHA\"}" | jq -r .id)
status=""
for _ in $(seq 90); do
  status=$(api GET "/deployments/$dep" | jq -r .status)
  [[ $status == RUNNING || $status == FAILED || $status == CANCELLED ]] && break
  sleep 2
done
if [[ $status != RUNNING ]]; then
  api GET "/deployments/$dep/logs" | jq -r '.items[]? | "\(.level) \(.message)"' | tail -30
  fail "deployment ended in ${status:-timeout}, want RUNNING"
fi

log "environment health"
health=$(api GET "/projects/$project/environments/staging/health")
jq -e '.status == "healthy"' <<<"$health" >/dev/null || fail "health: $(jq -c '{status, drift_reasons}' <<<"$health")"

printf '\nE2E PASSED: dashboard served, user registered, deployment %s RUNNING, environment healthy.\n' "$dep"
