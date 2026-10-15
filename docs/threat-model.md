# Threat model

CloudDeploy turns a tenant's git commit into running pods, so it holds the keys to a cluster. This document lists what an attacker would want, where trust changes hands, and which control answers each threat (STRIDE). Each control names where it lives, so it can be checked.

## Assets

| Asset | Why it matters |
|---|---|
| Cluster deploy rights (worker's ServiceAccount) | Can create Deployments in any tenant namespace |
| User accounts and sessions | Control over projects and production deploys |
| Database contents | Users, password hashes, refresh token hashes, audit trail |
| Runtime secrets | `DATABASE_URL`, `REDIS_URL`, `JWT_SECRET` |
| Release artifacts | Images and chart that every installation runs |

## Trust boundaries

```
browser ──► nginx (web) ──► API ──► PostgreSQL / Redis
                              │
tenant git repo ──► builder ──┤ (worker) ──► Kubernetes API ──► tenant namespaces
                              │
GitHub Actions ──► GHCR (signed images, chart) ──► cluster
```

1. **Internet → API.** Every request is untrusted.
2. **Tenant repository → builder.** A tenant's Dockerfile is untrusted code.
3. **Platform → tenant namespaces.** Tenants must not reach each other or the platform.
4. **CI/CD → registry → cluster.** Artifacts must come from this repository's pipeline.

## Threats and controls

| STRIDE | Threat | Control | Where |
|---|---|---|---|
| **Spoofing** | Credential stuffing or brute force | Argon2id hashing with bounded cost parameters; per-IP and per-account login limits (GCRA in Redis) | `internal/auth/password.go`, `internal/auth/ratelimits.go` |
| | Stolen refresh token | Rotation on every use; reuse of an old token revokes the whole family | `internal/auth/service.go`, [security.md](security.md) |
| | Session theft through XSS | Cookie mode: `HttpOnly; Secure; SameSite=Strict` refresh cookie; strict CSP on the dashboard and API | `internal/auth/handler.go`, `web/nginx/security-headers.conf` |
| | Workload impersonating another in Azure | One workload identity per ServiceAccount, federated to its exact subject | `infra/terraform/modules/keyvault` |
| **Tampering** | Lost updates on concurrent edits | ETag / `If-Match` optimistic concurrency | `internal/projects` |
| | Duplicate or replayed deployments | Idempotency keys; compare-and-swap state machine | `internal/deployments` |
| | Option injection into git, or swapping the commit through a URL fragment | `https` only; no credentials, query or fragment; commit SHA regex; git hooks disabled | `internal/projects/project.go`, `internal/builder/docker.go` |
| | Modified release artifacts | Images and chart signed keyless; CD verifies signatures and deploys **by digest**; actions pinned to SHAs | `.github/workflows/cd.yml`, [cd.md](cd.md) |
| **Repudiation** | Denying a production change | Audit log written in the same transaction as the change (actor, IP, metadata) and **append-only for the application role** | `internal/audit`, `migrations/00010_app_role.sql` |
| | Unattributable requests | Request IDs on every response; trace IDs in logs | `internal/middleware`, `internal/tracing` |
| **Information disclosure** | Internal errors leaking to clients | One error envelope; 5xx details only in server logs | `internal/httpx` |
| | Secrets in logs, state or the database | Log sanitizing; environment secrets stored as references only; Terraform write-only arguments | `internal/logsafe`, `internal/environments`, [ADR-007](adr/ADR-007-secrets-out-of-state.md) |
| | Tenant reading another tenant's data | Every query is scoped by organization membership; the namespace always comes from an authorized environment, never from request input | `internal/projects`, `internal/monitoring` |
| **Denial of service** | Request floods | Per-user API limit (600/min); request body limit; `DisallowUnknownFields` | `internal/ratelimit`, `internal/httpx/decode.go` |
| | Expensive work per request | Bounded Argon2 parameters; pagination limits; log output caps; build timeout and source size limit | `internal/auth`, `internal/builder` |
| | Redis outage locking users out | Rate limiting fails open (an availability trade-off, measured by `ratelimit_decisions_total{decision="fail_open"}`) | [ADR-003](adr/ADR-003-redis.md) |
| **Elevation of privilege** | Viewer or developer changing production | RBAC (admin > developer > viewer); production changes require admin | `internal/auth/rbac.go` |
| | Tenant code escaping the build | No Docker socket outside development (the chart refuses it in production); production builds run on ACR Tasks' isolated agents | [ADR-008](adr/ADR-008-image-builds.md), `templates/_helpers.tpl` |
| | Tenant pods escaping their namespace | Pod Security `baseline` enforced on tenant namespaces; platform pods `restricted`, distroless, non-root, read-only root filesystem | `internal/kube/manifests.go`, chart |
| | Worker abused for cluster takeover | ClusterRole limited to the verbs it uses: no Secrets, no exec, no RBAC | `templates/worker.yaml` |
| | Compromised API altering the schema or audit trail | Least-privilege database login; production refuses to start with excess privileges | [security.md](security.md#database-privileges) |
| | Compromised CI job | Workflow token `contents: read` by default; write scopes only on the jobs that publish; no stored secrets | `.github/workflows` |

## Continuous checks

| Check | Runs |
|---|---|
| govulncheck, Trivy (images), gitleaks (history), npm audit | Every CI run |
| CodeQL (`security-extended`, Go and TypeScript) | PRs, pushes to main, weekly |
| Fuzzing of parsers and validators | Nightly |
| Dependabot updates | Weekly |

## Out of scope

- Tenant application vulnerabilities. CloudDeploy runs tenant code; it doesn't audit it.
- A compromised cluster administrator or cloud account.
