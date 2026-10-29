-- +goose Up
CREATE TABLE IF NOT EXISTS feature_center_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    image_studio_enabled INTEGER NOT NULL CHECK (image_studio_enabled IN (0, 1)),
    ai_radar_enabled INTEGER NOT NULL CHECK (ai_radar_enabled IN (0, 1)),
    revision INTEGER NOT NULL CHECK (revision > 0),
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO feature_center_settings (id, image_studio_enabled, ai_radar_enabled, revision)
SELECT 1, 1, 1, 1
WHERE NOT EXISTS (SELECT 1 FROM feature_center_settings WHERE id = 1);

-- +goose Down
DROP TABLE IF EXISTS feature_center_settings;
