# CloudDeploy

A cloud-native deployment platform: register an application, define its environments, build it, deploy it to Kubernetes, and track status, health, logs and history — with Azure infrastructure provisioned by Terraform.

> **Status:** Phase 4 (organizations & projects) complete. The roadmap is built phase-by-phase; see [Roadmap](#roadmap).

## Architecture (target)

```
Web UI (React/TS) ──► Go API (REST, /api/v1) ──► PostgreSQL
                          │                  └─► Redis (queue, cache, rate limit)
                          ▼
                   Deployment worker ──► Kubernetes (Deployments, Services, Ingress)
```

It is a **modular monolith**: one deployable Go binary with clear internal package boundaries. See [ADR-004](docs/adr/ADR-004-modular-monolith.md) for why it isn't split into microservices.

## Tech stack

| Area | Choice |
|---|---|
| API | Go 1.27, standard library `net/http` routing, `log/slog` JSON logging |
| Data | PostgreSQL, Redis *(Phase 2 / 7)* |
| Runtime | Docker (distroless, non-root), Kubernetes + Helm *(Phase 10–11)* |
| Cloud | Terraform, Azure AKS/ACR *(Phase 17–18)* |
| CI/CD | GitHub Actions *(Phase 19–20)* |

## Quick start

Prerequisites: Go 1.27+, Docker (Colima works), GNU Make.

```bash
cp .env.example .env      # then replace every change-me value (openssl rand -hex 24)
make db-up                # Postgres on 127.0.0.1:15432
make migrate seed         # schema + dev data
make dev                  # run the API natively on :8080
curl localhost:8080/readyz
```

| Command | Purpose |
|---|---|
| `make test` | Unit tests with the race detector |
| `make test-integration` | Integration tests against real Postgres (`clouddeploy_test` database) |
| `make db-up` / `make migrate` / `make seed` / `make psql` | Database workflow (see [docs/database.md](docs/database.md)) |
| `make lint` | gofmt check and `go vet` |
| `make build` | Static binary in `bin/api` |
| `make docker-build` | Container image `clouddeploy-api` |
| `make docker-up` / `make docker-down` | Full stack in containers: Postgres, then migrate job, then API |

## Configuration

All configuration comes from environment variables (see [.env.example](.env.example)). The API validates every value at startup and exits with a list of all errors if any are invalid.

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
| `JWT_SECRET` | *(required)* | At least 32 bytes. Generate with `openssl rand -base64 48`. |
| `ACCESS_TOKEN_TTL` / `REFRESH_TOKEN_TTL` | `15m` / `720h` | Token lifetimes |

## Endpoints (so far)

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | **Liveness** check. The process is up. It never checks dependencies, so a database outage won't cause restart loops. |
| `GET /readyz` | **Readiness** check. Checks PostgreSQL and returns `503` if it's unreachable, so traffic is routed away from this instance. |
| `/api/v1/auth/*` | Register, login, refresh, logout, me |
| `/api/v1/organizations/*` | Organizations and member management (RBAC) |
| `/api/v1/projects/*` | Project CRUD with cursor pagination and ETag concurrency control |

The full reference is in [docs/api.md](docs/api.md).

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

## Roadmap

0 audit ✅ · 1 scaffolding ✅ · 2 database ✅ · 3 auth ✅ · 4 orgs/projects ✅ · 5 environments · 6 deployment domain · 7 Redis · 8 async worker · 9 image builds · 10 local k8s · 11 Helm · 12 monitoring/logs · 13 rollback · 14 dashboard · 15 test hardening · 16 observability · 17 Terraform · 18 Azure · 19 CI · 20 CD · 21 security · 22 performance · 23 polish

## Documentation

- [API reference](docs/api.md)
- [Architecture decisions](docs/adr/)
- [Local development](docs/local-development.md)
- [Database design, ERD and indexes](docs/database.md)
- [Security: authentication, tokens and RBAC](docs/security.md)
