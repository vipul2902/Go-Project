-- +goose Up
-- W3C traceparent of the request that created the deployment. The worker
-- continues that trace, so one trace spans API request -> queue -> build ->
-- rollout. Nullable: tracing is optional. ADD COLUMN without a default is a
-- metadata-only change.
ALTER TABLE deployments ADD COLUMN trace_context text CHECK (length(trace_context) <= 256);

-- +goose Down
ALTER TABLE deployments DROP COLUMN trace_context;
