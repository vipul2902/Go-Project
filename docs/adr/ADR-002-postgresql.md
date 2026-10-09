# ADR-002: PostgreSQL as the system of record

- **Status:** Accepted
- **Date:** 2026-10-09

## Context
The data is relational: organizations → projects → environments → deployments. Its integrity rules must hold even under concurrent requests. For example, no two deployments may run in the same environment at once, and secrets must never be stored as plaintext.

## Decision
- PostgreSQL is the database, accessed through `pgx/v5` and its connection pool.
- Migrations are managed by `goose` and embedded in the binary.
- Queries are hand-written and parameterized; there is no ORM.

## Rationale
- Partial unique indexes and CHECK constraints let the database enforce invariants that application code can't guarantee across replicas.
- PostgreSQL is managed on Azure (Flexible Server), so production needs no self-hosted database.
- `pgx` is the most actively maintained Go driver for PostgreSQL. It has a built-in pool and native PostgreSQL types.
- Plain SQL keeps queries visible and reviewable. Parameterization (`$1`) rules out SQL injection.

## Alternatives considered
- **An ORM (GORM):** it hides the SQL actually executed and makes partial indexes and locking hard to express.
- **sqlc:** it generates type-safe code from SQL. It's a strong option to revisit once the query count grows.
- **MySQL:** it has no partial indexes, and its CHECK constraints are weaker.

## Consequences
Hand-written SQL means more boilerplate for scanning rows. That cost is accepted in exchange for explicit control.
