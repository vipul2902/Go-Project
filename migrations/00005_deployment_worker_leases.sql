-- +goose Up
-- Worker leases: a claimed deployment records which worker owns it and
-- until when. The worker renews the lease while working; if the worker
-- dies, the lease expires and a reaper fails the deployment instead of
-- leaving it stuck in BUILDING/DEPLOYING forever.
ALTER TABLE deployments
    ADD COLUMN claimed_by       text,
    ADD COLUMN lease_expires_at timestamptz;

-- The claim query: oldest QUEUED first. Partial index stays tiny because
-- deployments are QUEUED only briefly.
CREATE INDEX deployments_queued_idx ON deployments (created_at, id) WHERE status = 'QUEUED';
-- The reaper query: active deployments ordered by lease expiry.
CREATE INDEX deployments_lease_idx ON deployments (lease_expires_at) WHERE status IN ('BUILDING', 'DEPLOYING');

-- +goose Down
DROP INDEX deployments_lease_idx;
DROP INDEX deployments_queued_idx;
ALTER TABLE deployments DROP COLUMN lease_expires_at, DROP COLUMN claimed_by;
