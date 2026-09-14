-- +goose Up
-- M7: the recursor cache memory budget (RecursionConfig.cache_max_bytes).
ALTER TABLE resolution_settings
    ADD COLUMN recursor_cache_max_bytes bigint NOT NULL DEFAULT 67108864
    CHECK (recursor_cache_max_bytes BETWEEN 4194304 AND 17179869184);

-- +goose Down
ALTER TABLE resolution_settings DROP COLUMN recursor_cache_max_bytes;
