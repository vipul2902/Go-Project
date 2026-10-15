# Performance

Every number below comes from `make perf` on an M1 MacBook Air (8 GB). The API, worker, PostgreSQL, Redis and the k6 load generator all ran on that one machine, so absolute numbers are conservative. The comparisons are the point.

## How it's measured

[`scripts/load/perf.sh`](../scripts/load/perf.sh) (`make perf`) builds the binaries, starts the API and a dry-run worker against the Compose PostgreSQL and Redis, and runs two [k6](https://k6.io) scenarios. It collects:
- k6 summaries
- a CPU profile of the API taken during the steady state (pprof, opt-in with `PPROF_ENABLED`, refused in production)
- PostgreSQL's per-query timings (`pg_stat_statements`)

| Scenario | What it does |
|---|---|
| [`api.js`](../scripts/load/api.js) | The dashboard's request mix (dashboard, projects, deployments, environments) at a **fixed arrival rate** (300 req/s), so slow responses show up as latency rather than as less load. 20 s ramp, then 60 s steady. |
| [`queue.js`](../scripts/load/queue.js) | Each virtual user keeps one deployment in flight (create, then wait for `RUNNING`). Measures the PostgreSQL queue and the worker, timed from the server's own timestamps. |

**Rate limits stay on.** Each simulated client has its own IP through `X-Forwarded-For` (the load generator is a trusted proxy, like an ingress controller) and stays under the 600/min per-user limit.

`DEPLOYMENTS=300000 make perf` first seeds one large organization ([`volume.sh`](../scripts/load/volume.sh)):
- 60 members
- 50 projects and 100 environments
- 300,000 deployments over 30 days
- 100,000 audit entries

The read load then runs as those members.

## Finding: the dashboard didn't scale with data

With near-empty tables everything looked fine. With a realistic organization it collapsed:

| 300 req/s read mix | Empty organization | 300k deployments, before | 300k deployments, after |
|---|---|---|---|
| p95 latency | 3.0 ms | **13.1 s** | **7.5–8.6 ms** |
| p99 latency | 4.8 ms | 13.6 s | 15–53 ms |
| Load sustained | 300 req/s | ~43 req/s (requests piled up) | 300 req/s |
| Errors | 0 | 0 | 0 |

`pg_stat_statements` pointed at two queries. Everything else stayed below 1 ms:

| Query | Before (mean under load) | After |
|---|---|---|
| Dashboard status counts, last 30 days | 424 ms | **1.4 ms** |
| Most recent deployments across your projects | 302 ms | **1.05 ms** |

The dashboard is 30% of the traffic. Those two queries tied up the API's 10 pooled connections, so every other request queued behind them.

### Fix 1: daily rollup for status counts

`EXPLAIN ANALYZE` showed a parallel sequential scan aggregating every deployment in the caller's organizations: 85 ms on an idle database, and much worse under concurrency. That cost grows with history forever.

Migration [`00011`](../migrations/00011_deployment_daily_counts.sql) adds `deployment_daily_counts`, one row per (project, UTC day, status):
- **Exact, not periodic.** Triggers maintain it in the same transaction as every insert, status change or delete, so it's never stale.
- **Bounded.** The dashboard sums at most projects × 30 × 6 rows, whatever the volume.
- **No deadlocks.** Status changes only move forward through the state machine, so concurrent transitions lock counter rows in a consistent order.
- **Cost.** One extra upsert per status change.
- **Semantics.** "Last 30 days" now means the last 30 UTC calendar days, including today.

An integration test drives concurrent deployments through every path: completed, failed and cancelled. It checks that the rollup equals a direct count and that the dashboard reports the same. A mutation check confirmed the test fails when the status trigger is disabled.

### Fix 2: top-N per project, then merge

"Newest 8 deployments across everything you can see" joined and sorted every deployment in those organizations. The list now takes the newest `limit+1` **per project** from the existing `(project_id, created_at)` index (a `LATERAL` subquery) and merges them: 50 projects × 9 rows instead of 300,000. Filters (environment, status) and cursor pagination move inside the per-project subquery, so every list query benefits, not only the dashboard.

## Deployment pipeline

Dry-run builds and deploys, so this measures the platform's own overhead (queue, leases, logs, state machine), not Docker or Kubernetes:

| Worker concurrency | Server-side create → `RUNNING` p95 | Completed per second |
|---|---|---|
| 2 (default) | 221 ms | 144 |
| 8 | **75 ms** | 145 |

The completed rate is the test's own ceiling: 40 clients polling every 250 ms. The latency difference is the signal. At concurrency 2, deployments wait for a free slot; at 8 they don't.

In real use a build takes seconds to minutes, so worker sizing comes down to build time × arrival rate, not platform overhead. Workers scale horizontally because claims use `FOR UPDATE SKIP LOCKED`.

## Sizing the API (HPA)

The CPU profile during the 300 req/s steady state showed the API process using **0.107 cores**: about 0.36 millicores per request, or roughly 2,800 req/s per core of Go CPU. The API is cheap, and database query cost decides capacity, as the dashboard finding showed. The production values follow from this:

| Setting | Value | Reasoning |
|---|---|---|
| `api.resources.requests.cpu` | 100m | About 280 req/s of CPU headroom per pod at the measured cost |
| HPA target | 70% CPU | A pod scales out at about 70m, roughly 200 req/s, leaving headroom for bursts |
| HPA min / max | 3 / 10 | Three replicas for availability across zones |
| `DB_MAX_CONNS` × max replicas | 10 × 10 = 100 | Must stay below the server's `max_connections`, together with the worker and migrate connections. Check the chosen PostgreSQL SKU's limit, and add PgBouncer before raising either number. |

## Keeping it fast

- **Nightly regression check.** The nightly workflow runs `DEPLOYMENTS=300000 make perf` on GitHub's runners. It fails if the read mix breaks p95 < 100 ms or p99 < 250 ms, or if the pipeline breaks p95 < 1 s. The results are uploaded as an artifact.
- **Micro-benchmarks.** `make bench` covers the hot paths: password hashing, JWT, client IP, log sanitizing, manifests.

## Reproduce

```bash
make db-up && make migrate
make perf                                  # empty-ish organizations
DEPLOYMENTS=300000 make perf               # one large organization
READS=0 VUS=40 WORKER_CONCURRENCY=8 make perf   # pipeline only
```

Results land in `.perf/`:

| File | Contents |
|---|---|
| `api.txt`, `queue.txt` | k6 summaries |
| `top.txt` | CPU hot spots |
| `queries.txt` | Queries by total time |
| `cpu.pprof` | Raw profile, for `go tool pprof -http :0 bin/api .perf/cpu.pprof` |
