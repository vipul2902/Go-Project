# ADR-004: Modular monolith first

- **Status:** Accepted
- **Date:** 2026-10-09

## Context
The domain has distinct areas: auth, projects, environments, deployments, audit and infrastructure. Microservices are a common default, but they add network hops, distributed transactions, per-service CI/CD and operational cost.

## Decision
The API ships as one Go binary. Each domain lives in its own package under `internal/` and exposes a small API to the other packages. Only the deployment worker may later run as a separate process, from the same codebase and image.

## Rationale
- A single team and a single database don't justify the cost of running separate services.
- In-process calls let multi-step changes share one PostgreSQL transaction, which is simpler to keep correct.
- Package boundaries keep a later extraction cheap if a real need appears, such as independent scaling of the workers.

## When to revisit
Revisit this decision if a component needs a different scaling profile, release cadence or failure isolation. The deployment worker is the most likely first candidate.
