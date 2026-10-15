-- Dashboard status counts from a per-project, per-day rollup.
--
-- Counting deployments by status over 30 days aggregated every row in the
-- caller's organizations on every dashboard load: 85 ms alone, 400+ ms under
-- load, with 300k deployments in one organization (Phase 22, docs/performance.md).
-- This table holds one row per (project, UTC day, status), kept exact by
-- triggers in the same transaction as each change, so the dashboard sums at
-- most projects x 30 x 6 rows whatever the deployment volume.
--
-- Status changes only move forward through the state machine, so concurrent
-- transitions lock counter rows in a consistent order (no deadlock cycles).

-- +goose Up
CREATE TABLE deployment_daily_counts (
    project_id uuid    NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    day        date    NOT NULL,
    status     text    NOT NULL,
    count      integer NOT NULL CHECK (count >= 0),
    PRIMARY KEY (project_id, day, status)
);

-- +goose StatementBegin
CREATE FUNCTION deployment_counts_apply() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        UPDATE deployment_daily_counts SET count = count - 1
        WHERE project_id = OLD.project_id
          AND day = (OLD.created_at AT TIME ZONE 'UTC')::date
          AND status = OLD.status;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        INSERT INTO deployment_daily_counts AS c (project_id, day, status, count)
        VALUES (NEW.project_id, (NEW.created_at AT TIME ZONE 'UTC')::date, NEW.status, 1)
        ON CONFLICT (project_id, day, status) DO UPDATE SET count = c.count + 1;
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER deployments_counts_insert_delete
    AFTER INSERT OR DELETE ON deployments
    FOR EACH ROW EXECUTE FUNCTION deployment_counts_apply();

CREATE TRIGGER deployments_counts_status
    AFTER UPDATE OF status ON deployments
    FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION deployment_counts_apply();

-- Backfill from existing rows.
INSERT INTO deployment_daily_counts (project_id, day, status, count)
SELECT project_id, (created_at AT TIME ZONE 'UTC')::date, status, count(*)
FROM deployments
GROUP BY 1, 2, 3;

-- +goose Down
DROP TRIGGER deployments_counts_status ON deployments;
DROP TRIGGER deployments_counts_insert_delete ON deployments;
DROP FUNCTION deployment_counts_apply();
DROP TABLE deployment_daily_counts;
