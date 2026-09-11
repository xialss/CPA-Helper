-- +goose Up
CREATE TABLE IF NOT EXISTS model_price_channel_aliases (
    auth_type VARCHAR(20) NOT NULL DEFAULT 'apikey',
    channel_brand VARCHAR(40) NOT NULL,
    channel_key VARCHAR(500) NOT NULL,
    label VARCHAR(200) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_model_price_channel_alias UNIQUE (auth_type, channel_brand, channel_key)
);

-- +goose Down
DROP TABLE IF EXISTS model_price_channel_aliases;
