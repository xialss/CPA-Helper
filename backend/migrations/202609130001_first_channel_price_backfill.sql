-- +goose Up
-- Recompute cached costs for requests made before their channel's first price.
UPDATE usage_analytics_state
SET pricing_version = pricing_version + 1,
    hourly_needs_rebuild = 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = 1;

-- +goose Down
UPDATE usage_analytics_state
SET pricing_version = pricing_version + 1,
    hourly_needs_rebuild = 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = 1;
