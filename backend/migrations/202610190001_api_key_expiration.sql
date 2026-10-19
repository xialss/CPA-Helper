-- +goose Up
ALTER TABLE user_api_keys ADD COLUMN expires_at DATETIME;
ALTER TABLE user_api_keys ADD COLUMN expiration_enforced_at DATETIME;
ALTER TABLE user_api_keys ADD COLUMN expiration_sync_error TEXT;
CREATE INDEX idx_user_api_keys_expiration ON user_api_keys(expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP INDEX idx_user_api_keys_expiration;
ALTER TABLE user_api_keys DROP COLUMN expiration_sync_error;
ALTER TABLE user_api_keys DROP COLUMN expiration_enforced_at;
ALTER TABLE user_api_keys DROP COLUMN expires_at;
