-- +goose Up
-- When a release fails during rollout, automatically queue a rollback to the
-- environment's last successful deployment. Default on: leaving a cluster
-- Deployment pointed at a broken spec is the riskier default.
ALTER TABLE environments ADD COLUMN auto_rollback boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE environments DROP COLUMN auto_rollback;
