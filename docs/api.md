# API Reference

Base path: `/api/v1`. All request and response bodies are JSON. Except under `/auth` (apart from `/auth/me`), every endpoint requires `Authorization: Bearer <access_token>`.

An OpenAPI specification will be generated in a later phase. Until then, this document and the integration tests define the contract.

## Conventions

### Errors

```json
{ "error": { "code": "VALIDATION_FAILED", "message": "request validation failed",
             "details": { "name": "must be lowercase ..." }, "request_id": "8f1c..." } }
```

| Status | When |
|---|---|
| 400 | Malformed JSON, unknown fields, or the wrong field type |
| 401 | Missing, invalid or expired access token |
| 403 | You're a member, but your role doesn't allow this action |
| 404 | The resource doesn't exist **or you can't see it**. Non-members always get 404, so existence isn't revealed. |
| 409 | Conflict, for example a name is taken or the org would lose its last admin |
| 412 | The `If-Match` version is stale (someone else changed the resource) |
| 413 | Request body over 1 MiB |
| 422 | Field validation failed (see `details`) |

Clients should branch on `code` (stable) rather than on `message` (human-readable, may change). Quote `request_id` when reporting a problem; it matches the server logs and the `X-Request-ID` response header.

### Pagination

List endpoints use **cursor (keyset) pagination**:

```
GET /api/v1/projects?limit=20&sort=name
→ { "data": [...], "next_cursor": "eyJzIjoibmFtZSIs..." }
GET /api/v1/projects?limit=20&sort=name&cursor=eyJzIjoibmFtZSIs...
→ { "data": [...], "next_cursor": null }   ← last page
```

- `limit` is 1–100 and defaults to 20.
- Treat cursors as opaque. A cursor is only valid with the `sort` it was created with.
- Pages are stable: rows inserted while you are paging don't cause skipped or duplicated results.

### Optimistic concurrency

`GET`, `POST` and `PATCH` on a project return an `ETag`. Send it back in `If-Match` on `PATCH`. If the project changed in the meantime, you get `412 PRECONDITION_FAILED` instead of silently overwriting someone else's change.

## Auth

| Method and path | Body | Result |
|---|---|---|
| `POST /auth/register` | `email, name, password, organization_name` | 201: user and tokens. The user becomes admin of a new organization. |
| `POST /auth/login` | `email, password` | 200: user and tokens. 401 `INVALID_CREDENTIALS` |
| `POST /auth/refresh` | `refresh_token` | 200: a new token pair (rotated). 401 `INVALID_REFRESH_TOKEN` |
| `POST /auth/logout` | `refresh_token` | 204 (idempotent) |
| `GET /auth/me` | none | The user and their organization memberships and roles |

## Organizations

| Method and path | Min. role | Notes |
|---|---|---|
| `GET /organizations` | (any) | The organizations you belong to, including your role in each |
| `POST /organizations` | (any) | `name`, optional `slug`. You become admin. 409 `SLUG_TAKEN` |
| `GET /organizations/{orgID}` | viewer | |
| `GET /organizations/{orgID}/members` | viewer | |
| `POST /organizations/{orgID}/members` | admin | `email, role`. The user must already be registered. 404 `USER_NOT_FOUND`, 409 `ALREADY_MEMBER` |
| `PATCH /organizations/{orgID}/members/{userID}` | admin | `role`. 409 `LAST_ADMIN` |
| `DELETE /organizations/{orgID}/members/{userID}` | admin | 409 `LAST_ADMIN` |
| `GET /organizations/{orgID}/audit` | admin | The audit trail, newest first. Optional filters: `action` and `resource_type`. Paginated. |

## Projects

| Method and path | Min. role | Notes |
|---|---|---|
| `GET /projects` | viewer | Query parameters: `organization_id`, `q` (name prefix), `sort` (`name`, `-name`, `created_at`, `-created_at`; default `-created_at`), `limit`, `cursor` |
| `POST /projects` | developer | `organization_id, name, repository_url`, optional `default_branch` (default `main`). Returns `Location` and `ETag`. |
| `GET /projects/{id}` | viewer | Returns `ETag` |
| `PATCH /projects/{id}` | developer | Partial update of `name`, `repository_url` and `default_branch`. Optional `If-Match`. |
| `DELETE /projects/{id}` | admin | Soft delete. History is kept and the name becomes reusable. 409 `DEPLOYMENT_IN_PROGRESS` while a deployment is active. |

Validation rules:
- `name` must be a DNS-1123 label (lowercase letters, digits and `-`, up to 63 characters), because it becomes a Kubernetes resource name.
- `repository_url` must be `https` with no embedded credentials, query or fragment.
- `default_branch` must be a valid git branch name.

## Environments

Base path: `/projects/{id}/environments`. `{env}` is `development`, `staging` or `production`.

| Method and path | Min. role | Notes |
|---|---|---|
| `GET /` | viewer | Listed in promotion order: development, staging, production |
| `POST /` | developer (**admin for production**) | `name`, plus optional `namespace` (default `<project>-dev`, `-staging` or `-prod`), `replicas` (0–20, default 1) and `resources` |
| `GET /{env}` | viewer | |
| `PATCH /{env}` | developer (**admin for production**) | `replicas` and/or `resources`. All four resource values are replaced together, so each request/limit pair is always validated as a pair. |
| `DELETE /{env}` | admin | 409 `ENVIRONMENT_HAS_DEPLOYMENTS` if it has deployment history |

`resources` uses Kubernetes quantities: `{"cpu_request":"100m","cpu_limit":"500m","memory_request":"64Mi","memory_limit":"256Mi"}`.
- CPU must be between 10m and 4000m, and memory between 16Mi and 8Gi.
- Each request must not exceed its limit.

The namespace must be a DNS-1123 label, must be unique across the whole platform, and must not be `default`, `clouddeploy` or start with `kube-`.

### Variables

Base path: `/projects/{id}/environments/{env}/variables`.

| Method and path | Min. role | Notes |
|---|---|---|
| `GET /` | viewer | Secret variables show only their `secret_ref`, never a value |
| `PUT /{KEY}` | developer (**admin for production**) | Takes exactly one of `{"value": "..."}` or `{"secret_ref": "<secret>/<key>"}`. Returns 201 when the variable is created and 200 when it's replaced. |
| `DELETE /{KEY}` | developer (**admin for production**) | |

Rules for variables:
- Keys are `UPPER_SNAKE_CASE`. The `CLOUDDEPLOY_` and `KUBERNETES_` prefixes are reserved.
- Values are limited to 32 KiB, and an environment can hold at most 100 variables.
- Keys that look like credentials (`*PASSWORD*`, `*SECRET*`, `*TOKEN*`, `*API_KEY*`, `*PRIVATE_KEY*`, `*CREDENTIAL*`, `*ACCESS_KEY*`) **must** use `secret_ref`.

## Deployments

Deployments are asynchronous: creating one returns `202 Accepted` with status `QUEUED`. The state machine and the idempotency guarantees are described in [deployment.md](deployment.md).

| Method and path | Min. role | Notes |
|---|---|---|
| `POST /deployments` | developer (**admin for production**) | Body: `project_id, environment, commit_sha`. Optional `Idempotency-Key` header. Returns 202 when created and 200 with `Idempotent-Replayed: true` on a retry. 409 `DEPLOYMENT_IN_PROGRESS` includes `details.active_deployment_id`. 422 `IDEMPOTENCY_KEY_REUSED` |
| `GET /deployments` | viewer | Filters: `project_id`, `environment`, `status`. Newest first, cursor pagination |
| `GET /deployments/{id}` | viewer | Includes `config` (the snapshot), `duration_seconds` and `triggered_by` |
| `POST /deployments/{id}/cancel` | developer (**admin for production**) | 409 `INVALID_TRANSITION` if the deployment has already finished |
