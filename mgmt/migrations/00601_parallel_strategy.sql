-- +goose Up
-- M6: the parallel upstream strategy races up to parallel_max upstreams (0 = every candidate, capped at 8).
ALTER TABLE resolver_settings DROP CONSTRAINT IF EXISTS resolver_settings_strategy_check;
ALTER TABLE resolver_settings
    ADD CONSTRAINT resolver_settings_strategy_check CHECK (strategy IN ('ordered', 'fastest', 'parallel')),
    ADD COLUMN parallel_max integer NOT NULL DEFAULT 0 CHECK (parallel_max BETWEEN 0 AND 8);

-- +goose Down
UPDATE resolver_settings SET strategy = 'fastest' WHERE strategy = 'parallel';
ALTER TABLE resolver_settings DROP COLUMN parallel_max;
ALTER TABLE resolver_settings DROP CONSTRAINT resolver_settings_strategy_check;
ALTER TABLE resolver_settings ADD CONSTRAINT resolver_settings_strategy_check CHECK (strategy IN ('ordered', 'fastest'));
