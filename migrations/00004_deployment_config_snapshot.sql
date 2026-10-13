-- +goose Up
-- The exact configuration a deployment used (namespace, replicas, resources,
-- variables incl. secret *references*). Captured at creation so history is
-- accurate after the environment changes, and so rollback can restore it.
-- ADD COLUMN with a constant DEFAULT is metadata-only in Postgres 11+: no
-- table rewrite, safe on large tables.
ALTER TABLE deployments ADD COLUMN config jsonb NOT NULL DEFAULT '{}';

-- Audit listing pages by id (monotonic), so index (organization_id, id).
-- The table is small at this stage; on a large production table this would
-- be a separate NO TRANSACTION migration using CREATE INDEX CONCURRENTLY.
CREATE INDEX audit_logs_org_id_idx ON audit_logs (organization_id, id DESC);
DROP INDEX audit_logs_org_created_idx;

-- +goose Down
CREATE INDEX audit_logs_org_created_idx ON audit_logs (organization_id, created_at DESC);
DROP INDEX audit_logs_org_id_idx;
ALTER TABLE deployments DROP COLUMN config;
