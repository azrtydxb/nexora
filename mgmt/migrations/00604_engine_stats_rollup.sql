-- +goose Up
-- The newest engine Stats sample per engine per 5 minutes, kept 8 days for the 7-day dashboard range.
CREATE TABLE engine_stats_rollup (
    engine_id uuid NOT NULL REFERENCES engines (id) ON DELETE CASCADE,
    bucket    timestamptz NOT NULL,
    stats     bytea NOT NULL,
    PRIMARY KEY (engine_id, bucket)
);

-- +goose Down
DROP TABLE engine_stats_rollup;
