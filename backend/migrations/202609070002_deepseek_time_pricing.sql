-- +goose Up
ALTER TABLE model_prices ADD COLUMN time_pricing TEXT;
ALTER TABLE model_price_versions ADD COLUMN time_pricing TEXT;
CREATE TABLE model_price_time_templates (
    name TEXT PRIMARY KEY,
    rule TEXT NOT NULL
);
INSERT INTO model_price_time_templates(name, rule) VALUES ('deepseek', '{"timezone":"Asia/Shanghai","peak_windows":[{"weekdays":[1,2,3,4,5],"start":"09:00","end":"12:00"},{"weekdays":[1,2,3,4,5],"start":"14:00","end":"18:00"}],"offpeak_mode":"multiplier","offpeak_multiplier":0.5,"long_context_multiplier":0.5}');

-- +goose Down
DROP TABLE IF EXISTS model_price_time_templates;
ALTER TABLE model_price_versions DROP COLUMN time_pricing;
ALTER TABLE model_prices DROP COLUMN time_pricing;
