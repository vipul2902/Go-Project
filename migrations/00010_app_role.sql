-- Least privilege for the running application.
--
-- The schema owner (the migrate job's login) keeps DDL. The API and worker
-- log in as a member of clouddeploy_app, which can read and write rows but
-- cannot alter or drop tables, read the migration history, or rewrite the
-- audit trail: audit_logs is append-only for the application, so a
-- compromised API cannot erase what it did.
--
-- clouddeploy_app is a group role (NOLOGIN). Login roles join it with
-- `migrate app-user` (see docs/security.md). Roles are cluster-wide, so the
-- role may already exist (another database, or an earlier run).

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'clouddeploy_app') THEN
        CREATE ROLE clouddeploy_app NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO clouddeploy_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO clouddeploy_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO clouddeploy_app;

-- Append-only audit trail; no access to migration bookkeeping.
REVOKE UPDATE, DELETE, TRUNCATE ON audit_logs FROM clouddeploy_app;
REVOKE ALL ON goose_db_version FROM clouddeploy_app;
REVOKE ALL ON SEQUENCE goose_db_version_id_seq FROM clouddeploy_app;

-- Tables created by later migrations (run as the owner) get the same DML
-- grants automatically. A migration that adds an append-only table must
-- revoke UPDATE/DELETE on it explicitly, as above.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO clouddeploy_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO clouddeploy_app;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM clouddeploy_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE USAGE, SELECT ON SEQUENCES FROM clouddeploy_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM clouddeploy_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM clouddeploy_app;
REVOKE USAGE ON SCHEMA public FROM clouddeploy_app;
-- The role itself stays: it is cluster-wide and may be used by other databases.
