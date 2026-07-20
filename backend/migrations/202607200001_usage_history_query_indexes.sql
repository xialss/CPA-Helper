-- +goose Up
CREATE INDEX IF NOT EXISTS ix_usage_records_failed_timestamp
ON usage_records(failed, timestamp);

CREATE INDEX IF NOT EXISTS ix_usage_records_usage_username_timestamp
ON usage_records(usage_username, timestamp);

-- +goose Down
DROP INDEX IF EXISTS ix_usage_records_usage_username_timestamp;
DROP INDEX IF EXISTS ix_usage_records_failed_timestamp;
