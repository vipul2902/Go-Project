-- +goose Up
-- Runtime contract between the platform and the app: which port it listens
-- on, and (optionally) an HTTP path that answers 2xx when it is ready to
-- serve. Without a path, probes fall back to a TCP check on the port.
ALTER TABLE environments
    ADD COLUMN port integer NOT NULL DEFAULT 8080 CHECK (port BETWEEN 1 AND 65535),
    ADD COLUMN health_check_path text CHECK (health_check_path ~ '^/[A-Za-z0-9._~/%-]{0,199}$');

-- +goose Down
ALTER TABLE environments DROP COLUMN health_check_path, DROP COLUMN port;
