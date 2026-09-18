-- +goose Up
-- AI radar: GPT-only IQ/efficiency observations sampled from codexradar.com.
-- Live snapshots are never persisted; only history observations are.
CREATE TABLE IF NOT EXISTS ai_radar_points (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    observed_at DATETIME NOT NULL,
    iq REAL,
    passed REAL,
    total REAL,
    average_price_usd REAL,
    average_minutes REAL,
    combined_cost_index REAL,
    average_agent_steps REAL,
    average_total_tokens REAL,
    cache_hit_rate REAL,
    runs_24h INTEGER,
    runs_48h INTEGER,
    runs_total INTEGER,
    origin TEXT NOT NULL DEFAULT 'live',
    created_at DATETIME NOT NULL
);

-- Upstream update time is the dedupe key, so re-observing the same upstream
-- revision cannot create a duplicate row.
CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_radar_points_identity
    ON ai_radar_points (model, effort, observed_at);

CREATE INDEX IF NOT EXISTS idx_ai_radar_points_series
    ON ai_radar_points (model, effort, observed_at DESC);

-- Feature-local state. Kept out of app_settings so a generic settings save can
-- never race with the one-time history backfill marker.
CREATE TABLE IF NOT EXISTS ai_radar_state (
    id INTEGER PRIMARY KEY,
    backfill_completed_at DATETIME,
    updated_at DATETIME NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS ai_radar_points;
DROP TABLE IF EXISTS ai_radar_state;
