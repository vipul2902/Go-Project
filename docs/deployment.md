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
The builder and the deployer are chosen independently with `WORKER_BUILDER` and `WORKER_DEPLOYER`, and neither has a default:

| | `dryrun` | Real implementation |
|---|---|---|
| Builder | Describes the image it would build | `docker` (Phase 9): see [Image builds](#image-builds) |
| Deployer | Describes the Kubernetes objects it would apply. Every line is tagged `[dry-run]`. | `kubernetes` (Phase 10): see [kubernetes.md](kubernetes.md) |

### Image builds

The `docker` builder runs these steps, streaming every line of output into the pipeline log (capped at 1,000 lines):

1. **Validate** again: the URL must be `https` with no credentials, and the commit must look like a SHA. This is defense in depth, because the builder passes these values to subprocesses.
2. **Reuse:** if `clouddeploy/<project>-<projectid8>:<sha>` already exists, skip the build. Redeploys and rollbacks become instant, and a retried build step is idempotent.
3. **Fetch** with a blobless partial clone (`--filter=blob:none`). This downloads all commits and trees, so a short SHA can be resolved with `rev-parse`, but only the checked-out commit's file contents. Git runs with no hooks, no global or system config, no `file://` transport and no terminal prompts, so private repositories fail fast instead of hanging.
4. **Pin:** `git checkout --detach <full sha>`. The build is reproducible, unlike building a branch head.
5. **Limits:** source tree at most 500 MB; a `Dockerfile` must be at the repository root.
6. **Build** with BuildKit (`docker build --progress=plain`). The image is labelled with `org.opencontainers.image.revision` (the full SHA), `.source`, `dev.clouddeploy.project-id` and `dev.clouddeploy.deployment-id`. No secrets or build arguments are passed.
7. **Cancel:** interrupting the build sends SIGINT to `docker`, which cancels the BuildKit build in the daemon (verified: no orphaned build processes), with a SIGKILL after 10 seconds.

Images stay in the local Docker daemon, with no registry. Phase 10's local cluster reads them directly; registries (GHCR/ACR) come with CI/CD and Azure.

The compose `worker` container uses `WORKER_BUILDER=dryrun`. Giving it real builds would mean mounting the Docker socket, which is root access to the host. Run `make worker` natively for real builds. The production approach is in [ADR-008](adr/ADR-008-image-builds.md).

### Pipeline logs

The worker writes each step to `deployment_logs`. Clients read them with `GET /deployments/{id}/logs?after=<last id>`, and each response includes `next_after` and the deployment's current `status`, so a client can tail a running deployment by polling.

## Rollback

A rollback is a **new deployment** with `rollback_of = <target>`. History stays append-only, and the audit trail shows who rolled back what, and why.

| | Behaviour |
|---|---|
| **What is restored** | The target's exact **snapshot**: its commit (so its image is reused, not rebuilt), replicas, resources, port, health path and variables, including secret *references*. |
| **What is not changed** | The environment's own settings. The response lists `warnings` describing how they differ (keys only, never values), because the *next* regular deployment will apply them again. Silently rewriting someone's settings, secret references included, would be the more surprising behaviour. |
| **Valid targets** | A deployment of the same environment that completed successfully (`RUNNING`) and isn't already the current release (`409 ALREADY_CURRENT`). A failed deployment isn't a valid target (`422 INVALID_ROLLBACK_TARGET`). |
| **Rules it follows** | The same as a deploy: developer (admin for production), one active deployment per environment, and an optional `Idempotency-Key`. |

Endpoints:
- `POST /deployments/{id}/rollback` restores deployment `{id}`.
- `POST /projects/{id}/environments/{env}/rollback` restores **the previous release**: the most recent successful deployment with a different commit from the current one. Returns `409 NO_PREVIOUS_RELEASE` if there isn't one.

### Auto-rollback

When a deployment fails **during rollout** (the cluster was changed and the rollout failed), the worker queues a rollback to the environment's last successful release. It's controlled per environment by `auto_rollback`, which defaults to `true`. Without it, the cluster Deployment would keep pointing at the broken spec: the old pods keep serving, but any restart or node drain would start broken pods.

| It does **not** auto-roll back when... | Because... |
|---|---|
| The build failed | Nothing was applied to the cluster |
| A user cancelled, the lease was lost, or the worker shut down | Those aren't release failures |
| The failed deployment was itself a rollback | That's how loops start; a human should investigate |
| There's no earlier successful release | There's nothing to restore |
| Another deployment became active in the meantime | One active deployment per environment |

Each outcome, whether queued or skipped with its reason, is written to the failed deployment's log. The rollback runs as a system action (`triggered_by: null`) and is audited as `deployment.auto_rollback`.

**Idempotency:** the key is `auto-rollback:<failed deployment id>`, so a retried worker can't queue two rollbacks. A test caught a subtle bug here. The retry recomputed "the latest good release", which by then was *the rollback itself*, so it asked for a different target under the same key and was rejected as a mismatched reuse. The fix is to look up an earlier result for the key **before** recomputing inputs that may have changed since.

Known gap: if the worker crashes between recording the failure and queueing the rollback, no rollback is queued. The deployment is `FAILED`, health shows `failing` with drift, and a manual rollback fixes it.

Verified in the cluster: adding a variable that references a missing Secret caused `CreateContainerConfigError`. The deployment failed fast, the auto-rollback restored the previous snapshot, and health returned to `healthy` with no drift.

## Audit log

Each audit entry records the actor, organization, action, resource, metadata, client IP, request ID and timestamp. Recorded actions:

`auth.registered`, `auth.login`, `auth.refresh_token_reuse`, `deployment.succeeded` and `deployment.failed` (recorded by the worker, so they have no user actor), `deployment.rollback`, `deployment.auto_rollback`, `organization.created`, `member.added`, `member.role_changed`, `member.removed`, `project.created`, `project.updated` (with from/to values for each changed field), `project.deleted`, `environment.created`, `environment.updated`, `environment.deleted`, `variable.set`, `variable.deleted`, `deployment.created`, `deployment.cancelled`.

What is and isn't recorded:
- Variable **values** are never recorded, not even plain ones; only the key and whether it's a secret.
- Logins are platform-level events (`organization_id` is NULL), so they don't appear in an organization's audit view.
- The client IP is the TCP peer address. `X-Forwarded-For` is ignored until a trusted proxy is configured, because any client can forge it.
