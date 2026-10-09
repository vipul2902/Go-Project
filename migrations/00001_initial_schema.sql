-- +goose Up

-- Keeps updated_at correct even when rows are changed outside the app.
-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text        NOT NULL CHECK (email = lower(email) AND email LIKE '%_@_%'),
    name          text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
-- Emails are stored lower-cased (see CHECK), so a plain unique index suffices.
CREATE UNIQUE INDEX users_email_key ON users (email);
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE organizations (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    slug       text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE organization_members (
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role            text        NOT NULL CHECK (role IN ('admin', 'developer', 'viewer')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);
-- PK covers "members of org"; this covers "orgs of user" (checked on every authz).
CREATE INDEX organization_members_user_id_idx ON organization_members (user_id);

CREATE TABLE projects (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            text        NOT NULL CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    repository_url  text        NOT NULL CHECK (repository_url ~ '^https://'),
    default_branch  text        NOT NULL DEFAULT 'main' CHECK (length(default_branch) BETWEEN 1 AND 255),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    -- Soft delete: deployment history and audit records must outlive the project.
    deleted_at      timestamptz
);
-- Name unique per org among live projects; a deleted name can be reused.
-- Leading organization_id also serves "list projects in org".
CREATE UNIQUE INDEX projects_org_name_key ON projects (organization_id, name) WHERE deleted_at IS NULL;
CREATE TRIGGER projects_updated_at BEFORE UPDATE ON projects FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE environments (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (name IN ('development', 'staging', 'production')),
    namespace    text        NOT NULL CHECK (namespace ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    replicas     integer     NOT NULL DEFAULT 1 CHECK (replicas BETWEEN 0 AND 20),
    cpu_request  text        NOT NULL DEFAULT '100m',
    cpu_limit    text        NOT NULL DEFAULT '500m',
    memory_request text      NOT NULL DEFAULT '64Mi',
    memory_limit text        NOT NULL DEFAULT '256Mi',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE TRIGGER environments_updated_at BEFORE UPDATE ON environments FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Plain config values are stored here. Secrets are NEVER stored: a secret row
-- holds only a reference (e.g. a Kubernetes Secret / Key Vault name).
CREATE TABLE environment_variables (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id uuid        NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    key            text        NOT NULL CHECK (key ~ '^[A-Z_][A-Z0-9_]{0,127}$'),
    value          text,
    is_secret      boolean     NOT NULL DEFAULT false,
    secret_ref     text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (environment_id, key),
    CONSTRAINT environment_variables_secret_separation CHECK (
        (is_secret AND value IS NULL AND secret_ref IS NOT NULL)
        OR (NOT is_secret AND value IS NOT NULL AND secret_ref IS NULL)
    )
);
CREATE TRIGGER environment_variables_updated_at BEFORE UPDATE ON environment_variables FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- text + CHECK instead of a Postgres ENUM: enum values cannot be removed or
-- reordered without recreating the type; a CHECK is changed in one migration.
CREATE TABLE deployments (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid        NOT NULL REFERENCES projects (id),
    environment_id  uuid        NOT NULL REFERENCES environments (id),
    commit_sha      text        NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{7,40}$'),
    image           text,
    status          text        NOT NULL DEFAULT 'QUEUED'
                    CHECK (status IN ('QUEUED', 'BUILDING', 'DEPLOYING', 'RUNNING', 'FAILED', 'CANCELLED')),
    triggered_by    uuid        REFERENCES users (id) ON DELETE SET NULL,
    idempotency_key text        CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    rollback_of     uuid        REFERENCES deployments (id),
    error_message   text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    completed_at    timestamptz,
    CHECK (completed_at IS NULL OR started_at IS NULL OR completed_at >= started_at),
    CHECK ((status IN ('RUNNING', 'FAILED', 'CANCELLED')) = (completed_at IS NOT NULL))
);
-- History views: "deployments for this environment/project, newest first".
CREATE INDEX deployments_environment_created_idx ON deployments (environment_id, created_at DESC);
CREATE INDEX deployments_project_created_idx ON deployments (project_id, created_at DESC);
-- Concurrency guarantee: at most ONE in-flight deployment per environment,
-- enforced by the database even with many API replicas racing.
CREATE UNIQUE INDEX deployments_one_active_per_env ON deployments (environment_id)
    WHERE status IN ('QUEUED', 'BUILDING', 'DEPLOYING');
-- Idempotency: a retried request with the same key maps to the same deployment.
CREATE UNIQUE INDEX deployments_idempotency_key ON deployments (environment_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE TABLE deployment_logs (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_id uuid        NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    level         text        NOT NULL CHECK (level IN ('debug', 'info', 'warn', 'error')),
    message       text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
-- Logs are read per deployment in insertion order, often "after id N" (tailing).
CREATE INDEX deployment_logs_deployment_id_idx ON deployment_logs (deployment_id, id);

-- Append-only: no updated_at, no FK cascade that could erase history.
CREATE TABLE audit_logs (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id uuid        REFERENCES organizations (id) ON DELETE SET NULL,
    actor_id        uuid        REFERENCES users (id) ON DELETE SET NULL,
    action          text        NOT NULL CHECK (action ~ '^[a-z_]+\.[a-z_]+$'),
    resource_type   text        NOT NULL,
    resource_id     text,
    metadata        jsonb       NOT NULL DEFAULT '{}',
    ip_address      inet,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_org_created_idx ON audit_logs (organization_id, created_at DESC);

-- Only a SHA-256 hash of each refresh token is stored; a DB leak exposes no
-- usable tokens. family_id links rotated tokens so reuse revokes the family.
CREATE TABLE refresh_tokens (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  bytea       NOT NULL UNIQUE,
    family_id   uuid        NOT NULL,
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    replaced_by uuid        REFERENCES refresh_tokens (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_family_id_idx ON refresh_tokens (family_id);

-- +goose Down
DROP TABLE refresh_tokens;
DROP TABLE audit_logs;
DROP TABLE deployment_logs;
DROP TABLE deployments;
DROP TABLE environment_variables;
DROP TABLE environments;
DROP TABLE projects;
DROP TABLE organization_members;
DROP TABLE organizations;
DROP TABLE users;
DROP FUNCTION set_updated_at();
