-- +goose Up
CREATE TABLE odoh_settings (
    id                 boolean PRIMARY KEY DEFAULT true CHECK (id),
    target_enabled     boolean NOT NULL DEFAULT false,
    proxy_enabled      boolean NOT NULL DEFAULT false,
    proxy_targets      jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(proxy_targets) = 'array'),
    proxy_timeout_ms   integer NOT NULL DEFAULT 2000 CHECK (proxy_timeout_ms BETWEEN 100 AND 10000),
    key_rotation_hours integer NOT NULL DEFAULT 24 CHECK (key_rotation_hours BETWEEN 1 AND 720),
    revision           bigint NOT NULL DEFAULT 1,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT odoh_proxy_targets CHECK (NOT proxy_enabled OR jsonb_array_length(proxy_targets) >= 1)
);
INSERT INTO odoh_settings DEFAULT VALUES;
CREATE TABLE odoh_keys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- NXE1 envelope from internal/secrets; never plaintext.
    seed_envelope bytea NOT NULL CHECK (substring(seed_envelope from 1 for 4) = 'NXE1'::bytea),
    created_at    timestamptz NOT NULL DEFAULT now(),
    publish_after timestamptz NOT NULL,
    not_after     timestamptz NOT NULL CHECK (not_after > publish_after)
);

-- +goose Down
DROP TABLE odoh_keys;
DROP TABLE odoh_settings;
