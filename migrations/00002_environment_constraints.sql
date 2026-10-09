-- +goose Up

-- Data migration: early rows stored secret references as
-- 'k8s:<namespace>/<secret>#<key>'. The canonical format is '<secret>/<key>'
-- (the namespace is always the environment's own). Convert before the new
-- CHECK is validated, so existing data stays valid.
UPDATE environment_variables
SET secret_ref = regexp_replace(secret_ref, '^k8s:[^/]+/([^#]+)#(.+)$', '\1/\2')
WHERE secret_ref LIKE 'k8s:%';

-- NOT VALID adds the constraint for new writes immediately without scanning
-- the table under an exclusive lock; VALIDATE then checks existing rows with
-- a lock that does not block reads or writes. On a large production table
-- this is the difference between a blip and an outage.
ALTER TABLE environment_variables
    ADD CONSTRAINT environment_variables_secret_ref_format
    CHECK (secret_ref IS NULL OR secret_ref ~ '^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?/[A-Za-z0-9._-]{1,253}$') NOT VALID;
ALTER TABLE environment_variables VALIDATE CONSTRAINT environment_variables_secret_ref_format;

ALTER TABLE environment_variables
    ADD CONSTRAINT environment_variables_value_size CHECK (octet_length(value) <= 32768) NOT VALID;
ALTER TABLE environment_variables VALIDATE CONSTRAINT environment_variables_value_size;

-- +goose Down
ALTER TABLE environment_variables DROP CONSTRAINT environment_variables_value_size;
ALTER TABLE environment_variables DROP CONSTRAINT environment_variables_secret_ref_format;
