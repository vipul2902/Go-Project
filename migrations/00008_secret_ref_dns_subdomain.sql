-- +goose Up
-- Tighten the secret_ref CHECK to Kubernetes' actual rules (found by a
-- differential fuzz test): the Secret name is a DNS-1123 subdomain (dot-
-- separated labels, so no "a..b"), and the key is [-._a-zA-Z0-9]+ but not
-- "." or "..". Same NOT VALID + VALIDATE pattern as migration 00002.
ALTER TABLE environment_variables DROP CONSTRAINT environment_variables_secret_ref_format;
ALTER TABLE environment_variables
    ADD CONSTRAINT environment_variables_secret_ref_format CHECK (
        secret_ref IS NULL OR (
            secret_ref ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[-._a-zA-Z0-9]+$'
            AND length(split_part(secret_ref, '/', 1)) <= 253
            AND length(split_part(secret_ref, '/', 2)) <= 253
            AND split_part(secret_ref, '/', 2) NOT IN ('.', '..')
        )
    ) NOT VALID;
ALTER TABLE environment_variables VALIDATE CONSTRAINT environment_variables_secret_ref_format;

-- +goose Down
ALTER TABLE environment_variables DROP CONSTRAINT environment_variables_secret_ref_format;
ALTER TABLE environment_variables
    ADD CONSTRAINT environment_variables_secret_ref_format
    CHECK (secret_ref IS NULL OR secret_ref ~ '^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?/[A-Za-z0-9._-]{1,253}$');
