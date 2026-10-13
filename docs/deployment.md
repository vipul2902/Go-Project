# Deployment Domain

A **deployment** is one attempt to ship a commit to one environment of a project. It is created by the API, advanced by the worker (Phase 8), and may be cancelled by a user.

## State machine

```
QUEUED ──► BUILDING ──► DEPLOYING ──► RUNNING
   │           │            │
   ├───────────┴────────────┴──► FAILED
   └───────────┴────────────┴──► CANCELLED
```

| From | Allowed to |
|---|---|
| QUEUED | BUILDING, FAILED, CANCELLED |
| BUILDING | DEPLOYING, FAILED, CANCELLED |
| DEPLOYING | RUNNING, FAILED, CANCELLED |
| RUNNING, FAILED, CANCELLED | none; these states are terminal |

A finished deployment never changes. A redeploy or a rollback creates a **new** deployment record, so history is append-only.

The machine is defined once, in [internal/deployments/status.go](../internal/deployments/status.go), and enforced in three layers:

1. **Go:** `CanTransition`, tested against all 36 `(from, to)` pairs.
2. **SQL compare-and-swap:** `UPDATE … SET status = $to WHERE id = $id AND status = ANY(<allowed predecessors of $to>)`. There's no "read the status, then write" step, so two actors racing (a user's cancel vs the worker) can't both succeed; the loser gets `409 INVALID_TRANSITION` with the real current state.
3. **Schema:** a CHECK constraint requires `completed_at` to be set exactly when the status is terminal, and `completed_at >= started_at`.

### A bug the race test caught

`TestCancelRacesWorker` once failed on the `completed_at >= started_at` constraint. The timestamps were set with `now()`, which in PostgreSQL is the **transaction start time**:
1. The cancel transaction began.
2. It blocked on the row lock held by the worker's `QUEUED → BUILDING` update.
3. Once the worker committed, the cancel re-checked its WHERE clause against the new row (still a valid transition) and stamped `completed_at` with its own *earlier* start time.

The fix is `clock_timestamp()`, which returns the actual current time. The constraint stopped a corrupt row from being written, which is why the bug showed up as a test failure rather than bad data.

## Idempotency

**What:** an operation is idempotent if repeating it has the same effect as doing it once.

**Why:** networks lose responses. A client that times out can't tell whether its `POST` failed or succeeded, so it retries. Users double-click. Without idempotency, either one starts a second production deployment.

**How:** clients send an `Idempotency-Key` header, typically a UUID generated once per user action.

| Situation | Result |
|---|---|
| First request with key K | `202 Accepted`, deployment created |
| Retry with K, same body | `200 OK` with the **same** deployment and `Idempotent-Replayed: true` |
| K reused with a different commit | `422 IDEMPOTENCY_KEY_REUSED`, so a mistake is never replayed silently |
| 10 concurrent requests with K | Exactly one `202`; the others get `200` with the same ID |

The database enforces this with a partial unique index on `(environment_id, idempotency_key)`. When concurrent requests race, the losers' inserts fail with a unique violation, and the service then looks up and returns the winner's deployment.

A second subtlety: those losers violate *two* unique indexes (the idempotency key and one-active-per-environment), and PostgreSQL may report either. So a one-active violation on a request that has a key is first checked as a possible replay. `TestConcurrentIdempotentRequests` caught this case.

Without a key, the **one active deployment per environment** index still blocks duplicates while a deployment is running (`409 DEPLOYMENT_IN_PROGRESS`, which includes `active_deployment_id`). It can't stop a duplicate after the first deployment finishes. That's what the key is for.

## Configuration snapshot

At creation, the environment's namespace, replica count, resources and variables are copied into `deployments.config`:
- Secret variables are copied as references only.
- The snapshot is never updated, so history shows *exactly* what was deployed.
- Rollback (Phase 13) redeploys a previous snapshot.

## Consistency guarantees

| Guarantee | Mechanism |
|---|---|
| At most one in-progress deployment per environment | Partial unique index `deployments_one_active_per_env` |
| No duplicate deployment from retries | Partial unique index `deployments_idempotency_key`, plus replay |
| No invalid state jumps | Compare-and-swap `UPDATE`, plus the CHECK constraints |
| A project can't be deleted while a deployment is being created or running | Creation takes `FOR SHARE` on the project row; deletion takes `FOR UPDATE` and checks for active deployments |
| Every change has an audit record, and vice versa | The audit row is written in the same transaction as the change |

## Asynchronous worker

`POST /deployments` only records the deployment and returns `202 Accepted`. A separate **worker** process (`cmd/worker`, same image) does the slow work. It's a separate process so that builds never compete with API requests and the two scale independently (ADR-004).

### PostgreSQL is the queue

```
API tx:  INSERT deployment (QUEUED) ; pg_notify('deployment_queued', id) ; COMMIT
                                                   │ delivered only on commit
worker:  LISTEN ◄──────────────────────────────────┘   (+ poll every 5s as fallback)
         UPDATE … SET status='BUILDING', claimed_by=me, lease=now+30s
           WHERE id = (SELECT … WHERE status='QUEUED' … FOR UPDATE SKIP LOCKED LIMIT 1)
```

| Concern | How it's handled |
|---|---|
| **Dual write** (row committed but message lost, or the reverse) | There is no second system. The queued row *is* the job, and `NOTIFY` is transactional. |
| **Exactly one worker per job** | The row lock plus PostgreSQL re-checking `status = 'QUEUED'` after a lock wait. Tested with 3 workers, each running 3 loops, competing for 12 jobs. |
| **Claimers blocking each other** | `SKIP LOCKED` lets a claimer skip a row another claimer is holding and take the next job immediately. `TestClaimSkipsLockedRowsWithoutBlocking` fails without it: the claim blocks for 500 ms and times out. |
| **Latency** | `LISTEN/NOTIFY` wakes an idle worker (measured at 89 ms from request to `RUNNING`). If the listener connection drops, polling covers the gap. Notifications only speed things up; correctness never depends on them. |
| **Worker crash** | A claim carries a **lease**. The heartbeat renews it every `LEASE_TTL/3`. Any worker's reaper fails deployments whose lease has expired (`worker lost: lease expired…`). They aren't retried automatically until every step is proven idempotent, so the user redeploys. |
| **User cancels mid-build** | The next heartbeat sees the deployment is no longer active and cancels the step's `context` (stopped in under a second in tests). The worker never overwrites `CANCELLED` with `FAILED`. |
| **Executor panics** | The panic is recovered, the deployment is marked `FAILED`, and the worker keeps running. |
| **SIGTERM** | The worker stops claiming. In-flight jobs get `SHUTDOWN_TIMEOUT` to finish, then are interrupted and recorded as `FAILED` ("interrupted: the worker shut down…"). Final writes use a context that the shutdown can't cancel. |

Why not Redis or Kafka for the queue? Both reintroduce the dual-write problem, which then needs a transactional outbox to solve. At our scale, a few deployments per minute, PostgreSQL handles the queue comfortably. Reconsider when throughput reaches thousands of jobs per second, or when other consumers need the same events (that would be the event-driven or Kafka option).

### Executors

The worker drives an `Executor` with two methods, `Build` and `Deploy`:
- **`dryrun`** (current) describes the image it would build and the Kubernetes objects it would apply. Every log line is tagged `[dry-run]`. It must be selected explicitly with `DEPLOY_EXECUTOR=dryrun`; there's no default.
- **Docker builder** (Phase 9) and **Kubernetes deployer** (Phase 10) will replace it.

### Pipeline logs

The worker writes each step to `deployment_logs`. Clients read them with `GET /deployments/{id}/logs?after=<last id>`, and each response includes `next_after` and the deployment's current `status`, so a client can tail a running deployment by polling.

## Audit log

Each audit entry records the actor, organization, action, resource, metadata, client IP, request ID and timestamp. Recorded actions:

`auth.registered`, `auth.login`, `auth.refresh_token_reuse`, `deployment.succeeded` and `deployment.failed` (recorded by the worker, so they have no user actor), `organization.created`, `member.added`, `member.role_changed`, `member.removed`, `project.created`, `project.updated` (with from/to values for each changed field), `project.deleted`, `environment.created`, `environment.updated`, `environment.deleted`, `variable.set`, `variable.deleted`, `deployment.created`, `deployment.cancelled`.

What is and isn't recorded:
- Variable **values** are never recorded, not even plain ones; only the key and whether it's a secret.
- Logins are platform-level events (`organization_id` is NULL), so they don't appear in an organization's audit view.
- The client IP is the TCP peer address. `X-Forwarded-For` is ignored until a trusted proxy is configured, because any client can forge it.
