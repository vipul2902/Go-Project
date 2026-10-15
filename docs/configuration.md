# Configuration

All configuration comes from environment variables (see [.env.example](../.env.example)). The API validates every value at startup and exits with a list of all errors if any are invalid.

| Variable | Default | Notes |
|---|---|---|
| `APP_ENV` | `development` | `development` \| `staging` \| `production` |
| `PORT` | `8080` | |
| `LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `HTTP_READ_TIMEOUT` / `HTTP_WRITE_TIMEOUT` / `HTTP_IDLE_TIMEOUT` | `10s` / `15s` / `60s` | Go duration syntax |
| `SHUTDOWN_TIMEOUT` | `20s` | Maximum time allowed to drain in-flight requests on SIGTERM |
| `DATABASE_URL` | *(required)* | PostgreSQL connection string |
| `DB_MAX_CONNS` / `DB_MIN_CONNS` | `10` / `1` | Pool bounds. Keep `replicas × max` below the server's `max_connections`. |
| `DB_MAX_CONN_LIFETIME` / `DB_MAX_CONN_IDLE_TIME` | `30m` / `5m` | Connection recycling |
| `METRICS_PORT` | `9090` | Internal Prometheus endpoint (`0` disables it); never exposed publicly |
| `PPROF_ENABLED` | `false` | Also serve `/debug/pprof` on the metrics port, for load tests. Refused in production. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | *(empty)* | Enables tracing, for example `http://localhost:4318` (Jaeger). Empty means no tracing and no cost. |
| `KUBE_CONTEXT` | *(empty)* | API: the cluster for monitoring when running outside one (empty means monitoring returns 503). Worker: required outside a cluster. |
| `TRUSTED_PROXY_CIDRS` | *(empty)* | Proxies allowed to set `X-Forwarded-For`, read from the right (see [security.md](security.md)) |
| `REDIS_URL` | *(required)* | Used for rate limiting. Startup fails if it's unreachable; at runtime an outage fails open (see [ADR-003](adr/ADR-003-redis.md)). |
| `JWT_SECRET` | *(required)* | At least 32 bytes. Generate with `openssl rand -base64 48`. |
| `ACCESS_TOKEN_TTL` / `REFRESH_TOKEN_TTL` | `15m` / `720h` | Token lifetimes |

Worker settings (used by `cmd/worker`):

| Variable | Default | Notes |
|---|---|---|
| `WORKER_BUILDER` | *(required)* | `docker` builds images with the local git and docker CLIs; `acr` builds on ACR Tasks (needs `ACR_REGISTRY_ID`); `dryrun` simulates the build. Required on purpose, so nobody mistakes simulated builds for real ones. |
| `WORKER_DEPLOYER` | *(required)* | `kubernetes` applies the app and waits for the rollout; `dryrun` simulates it |
| `KUBE_CONTEXT` | *(required outside a cluster)* | The kubeconfig context to deploy to, for example `colima`. It must be explicit so the worker never deploys to the wrong cluster. |
| `BUILD_WORKDIR` | OS temp dir | Where temporary checkouts are created; each build's directory is deleted afterwards |
| `WORKER_CONCURRENCY` | `2` | Maximum number of deployments processed in parallel (1–32) |
| `WORKER_LEASE_TTL` | `30s` | How long a claim survives without heartbeats before the reaper fails it |
| `WORKER_POLL_INTERVAL` | `5s` | Fallback polling; `NOTIFY` normally wakes the worker immediately |
| `WORKER_PORT` | `8081` | `/healthz` and `/readyz` for Kubernetes probes |

## Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | **Liveness** check. The process is up. It never checks dependencies, so a database outage won't cause restart loops. |
| `GET /readyz` | **Readiness** check. Returns `503` if PostgreSQL is unreachable, so traffic is routed away from this instance. If Redis is down it returns `degraded` with status 200, because the API still works with rate limiting off. |
| `/api/v1/auth/*` | Register, login, refresh, logout, me |
| `/api/v1/organizations/*` | Organizations and member management (RBAC) |
| `/api/v1/projects/*` | Project CRUD with cursor pagination and ETag concurrency control |
| `/api/v1/projects/{id}/environments/*` | Environments (replicas, resources, namespace) and variables. Secrets are stored as references only. |
| `/api/v1/deployments/*` | Create (idempotent and asynchronous), list, get, cancel and **roll back** deployments, and pipeline logs (`?follow=true` streams them) |
| `/api/v1/organizations/{id}/audit` | Audit trail (admin only) |
| `/api/v1/projects/{id}/environments/{env}/health` | Live health, pod status and drift, read from Kubernetes |
| `/api/v1/projects/{id}/environments/{env}/logs` | Application logs, with `?follow=true` for live streaming (SSE) |

The full reference is in [api.md](api.md).

Example:

```bash
curl -s -XPOST localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"developer@clouddeploy.local","password":"<SEED_PASSWORD from .env>"}'
```

All errors use one envelope:

```json
{ "error": { "code": "NOT_FOUND", "message": "resource not found", "request_id": "..." } }
```

Every response carries an `X-Request-ID` header. If the client sends a well-formed ID, the API reuses it. That way a single ID can be traced across services and log lines.
