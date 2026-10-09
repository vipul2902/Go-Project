# Database

PostgreSQL 17. The schema lives in [migrations/](../migrations/) and is embedded into the `migrate` binary.

## Entity relationships

```
organizations 1─┬─< organization_members >─1 users 1─< refresh_tokens
                │        (role)                │
                └─< projects 1─< environments 1─< environment_variables
                        │             │
                        └──────< deployments >──── triggered_by ── users
                                      │  └─ rollback_of ─> deployments
                                      └─< deployment_logs

audit_logs ── organization_id, actor_id (nullable, SET NULL: history outlives entities)
```

## Design decisions

| Decision | Why |
|---|---|
| UUID primary keys (`gen_random_uuid()`) | IDs appear in URLs, so they must not be guessable or reveal how many rows exist. They can also be generated in the app before an insert. |
| `bigint` identity keys for `deployment_logs` and `audit_logs` | These are high-volume, append-only tables. A sequential key keeps inserts cheap and gives a natural ordering for "after id N" pagination. |
| `text` + `CHECK` instead of `ENUM` | A value can't be removed from an ENUM, or the values reordered, without rebuilding the type. A CHECK constraint is changed in one ordinary migration. |
| Soft delete for projects only | Deployment history and audit records must outlive the project. Other tables are hard-deleted or cascade. |
| Secrets are never stored | An `environment_variables` row holds either a `value` or a `secret_ref` pointing to a Kubernetes Secret or Key Vault entry, never both. A CHECK constraint enforces this. |
| Refresh tokens are stored as SHA-256 hashes | A database leak doesn't expose usable tokens. `family_id` supports rotation with reuse detection (Phase 3). |
| `updated_at` is maintained by a trigger | It stays correct even when rows are changed outside the app, for example by manual SQL. |

## Indexes and why they exist

| Index | Serves |
|---|---|
| `users_email_key` (unique) | Login lookup. Emails are lower-cased by a CHECK constraint, so a plain index is enough. |
| `organization_members_user_id_idx` | "Which orgs is this user in?", which is asked on every authorization check. The primary key `(organization_id, user_id)` already covers the reverse question. |
| `projects_org_name_key` (unique, partial `WHERE deleted_at IS NULL`) | Project names are unique per org among live projects, so a deleted name can be reused. It also serves "list projects in org". |
| `deployments_environment_created_idx`, `deployments_project_created_idx` | Deployment history, newest first, without a sort step. |
| `deployments_one_active_per_env` (unique, partial on QUEUED/BUILDING/DEPLOYING) | **Concurrency guarantee.** At most one in-flight deployment per environment, enforced by the database even when many API replicas race. |
| `deployments_idempotency_key` (unique, partial) | A retried `POST /deployments` with the same key cannot create a second deployment. |
| `deployment_logs_deployment_id_idx (deployment_id, id)` | Tailing logs: `WHERE deployment_id = $1 AND id > $2 ORDER BY id`. |
| `audit_logs_org_created_idx` | The audit page: an org's events, newest first. |

Every index slows down writes and uses disk, so each one above exists to serve a known query.

## Concepts

- **Transactions:** a group of statements that all commit or all roll back. The seed command uses one, so a half-loaded fixture set can't exist.
- **Isolation:** we use the PostgreSQL default, `READ COMMITTED`. Rules such as "one active deployment" are enforced by unique indexes, not by application code that reads and then writes. A read-then-write check races under `READ COMMITTED`, while a unique index cannot. `TestConcurrentDeploysOnlyOneWins` proves this.
- **Constraints:** `CHECK`, `UNIQUE` and foreign keys are the last line of defence. They hold no matter which code path, or which buggy release, writes the data.
- **Connection pooling:** opening a PostgreSQL connection costs a process fork plus authentication, and each connection uses about 5–10 MB on the server. `pgxpool` reuses up to `DB_MAX_CONNS` connections. Keep `replicas × DB_MAX_CONNS` below the server's `max_connections`, which is 50 locally.
- **Migration strategy:** migrations are forward-only in production and each has a tested `Down` for development. `TestMigrationsRoundTrip` runs down then up. Migrations run as a separate step (a Compose one-off now, a Kubernetes Job later), never on API boot, because several replicas booting at once would race each other.

## Zero-downtime migration patterns (used in 00002 and 00003)

| Pattern | Why |
|---|---|
| A data migration runs **before** a new constraint is validated (00002 rewrites legacy `k8s:ns/secret#key` references to `secret/key`) | Without it, the new constraint would fail on existing rows. `TestMigrationConvertsLegacySecretRefs` migrates old-shape data forward. |
| `ADD CONSTRAINT … NOT VALID` then `VALIDATE CONSTRAINT` | Adding a CHECK normally scans the whole table under an exclusive lock that blocks writes. `NOT VALID` applies the rule to new writes immediately, and `VALIDATE` checks old rows under a lock that doesn't block reads or writes. |
| `CREATE UNIQUE INDEX CONCURRENTLY` in a `-- +goose NO TRANSACTION` migration | Builds the index without blocking writes. It can't run inside a transaction, so the migration holds only that one statement. If it fails partway, PostgreSQL leaves an `INVALID` index behind, which must be dropped before retrying. |

The environment namespace index (`environments_namespace_key`) guarantees that no two projects target the same Kubernetes namespace. Environments of soft-deleted projects keep their namespace reserved, because those Kubernetes resources may still exist.

## Commands

```bash
make db-up                 # Postgres only on 127.0.0.1:15432
make migrate               # apply migrations (CMD=down|reset|status)
make seed                  # idempotent dev data
make test-integration      # runs against clouddeploy_test, never dev data
make psql                  # SQL shell
```

The host port is **15432**, not 5432, because the reference machine already runs native PostgreSQL 16 and 17 on 5432 and 5433.

## Development seed accounts

The seed creates `admin@clouddeploy.local`, `developer@clouddeploy.local` and `viewer@clouddeploy.local`, all with the password set in `SEED_PASSWORD` in `.env`. These are development-only accounts, and `cmd/seed` refuses to run unless `APP_ENV=development`.
