-- CREATE INDEX CONCURRENTLY cannot run inside a transaction, so this
-- migration is non-transactional and contains only this one statement
-- (if it fails, nothing else is half-applied).
-- +goose NO TRANSACTION

-- +goose Up
-- Two projects must never target the same Kubernetes namespace: their
-- deployments would overwrite each other. CONCURRENTLY builds the index
-- without blocking writes to the table.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS environments_namespace_key ON environments (namespace);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS environments_namespace_key;
