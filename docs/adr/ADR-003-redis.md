# ADR-003: Redis, and what we deliberately don't use it for

- **Status:** Accepted
- **Date:** 2026-10-13

## Context
Redis is often added by reflex. Our rule is that every use must solve a problem we actually have. When Phase 7 began, the candidates were rate limiting, caching, the deployment job queue and distributed locks.

## Decision

| Use | Decision | Reason |
|---|---|---|
| **Rate limiting** | Adopted (Phase 7) | Limits must be shared across API replicas. In-process counters would multiply the limit by the replica count. |
| **Job queue and job state** | **Rejected in Phase 8** | Using Redis would create a dual write: the deployment row in PostgreSQL plus a message in Redis, which can't be atomic. PostgreSQL itself is the queue (`SKIP LOCKED` and leases), with `NOTIFY` as a transactional wake-up. See [deployment.md](../deployment.md). |
| **Distributed locks** | **Not needed (Phase 8)** | PostgreSQL row locks and worker leases cover mutual exclusion, and the unique index guarantees one active deployment per environment. A second locking system would add failure modes without adding guarantees. |
| **Caching project and role lookups** | **Rejected for now** | See below. |

### Why there's no cache yet
- **No measured need.** Authorization is a single indexed PostgreSQL join that takes well under a millisecond. A cache adds a network round trip of similar cost and saves little.
- **Security cost.** The hot lookups are *authorization data*. With cache-aside and a TTL, a demoted or removed member keeps their old rights until the entry expires or is invalidated. Delete-after-commit invalidation narrows that window but can't close it: a reader that loaded the old value just before the commit can write it back into the cache.
- **Revisited after load testing** ([performance.md](../performance.md)). The one hot read path, the dashboard, was database-bound by query shape, not by round trips: a rollup table and per-project top-N queries took it from 424 ms to 1.4 ms. A cache would have hidden the cost rather than removed it, so Redis remains limited to rate limiting. Live status uses server-sent events instead of polling.

For reference, cache-aside works like this: read the cache; on a miss, load from the database and write the value to the cache with a TTL; on a write, update the database and then delete the cache key. Stampedes are prevented with `singleflight`, and a Redis outage falls back to the database.

## Rate limiting design
- **Algorithm:** GCRA in a Lua script. It stores one key per subject holding a single timestamp, the theoretical arrival time.
  - It has no burst at window boundaries, unlike fixed windows, and smooths traffic while allowing a configured burst.
  - The check and the update are atomic, so the limit is exact across replicas (tested with 200 concurrent requests from two clients against a limit of 50).
  - It uses Redis's own clock, so clock drift between replicas can't make them disagree.
  - Each key expires when its limit has fully refilled, so idle keys cost no memory.
- **Failure policy: fail open.** If Redis is unavailable, requests are allowed and a warning is logged. `/readyz` reports `degraded` but stays 200, so Kubernetes keeps the pod in service. Rate limiting protects the API; losing the protection shouldn't take the API down. The trade-off is that brute-force protection is absent during a Redis outage; monitoring and alerting come in Phase 16.
- **Memory policy:** `maxmemory 64mb` with `volatile-lru`, which evicts only keys with a TTL. Every rate-limit key has one, so all of them can be evicted under memory pressure. Keys written without a TTL would be protected from eviction.
- **Persistence:** off. Losing rate-limit state on a restart only restarts the limit windows. (Phase 8 put the queue in PostgreSQL, so Redis holds no data that needs to survive a restart.)

## Consequences
- Redis is a required dependency at startup (fail fast on misconfiguration), but only an optional one at runtime.
- Behind a load balancer, set `TRUSTED_PROXY_CIDRS` (added in Phase 11) so per-IP limits apply to real clients rather than to the proxy. `X-Forwarded-For` is only honoured from trusted proxies, read from the right.
