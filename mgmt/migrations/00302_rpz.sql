-- +goose Up
CREATE TABLE rpz_zones (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 text NOT NULL UNIQUE,
    position             integer NOT NULL,
    source_type          text NOT NULL CHECK (source_type IN ('file', 'transfer')),
    blob_sha256          text REFERENCES blobs (sha256),
    file_records         integer,
    primary_address      text,
    tsig_key_name        text,
    tsig_algorithm       text CHECK (tsig_algorithm IN ('hmac-sha256', 'hmac-sha512')),
    -- NXE1 envelope from internal/secrets; never plaintext.
    tsig_secret_envelope bytea CHECK (tsig_secret_envelope IS NULL OR substring(tsig_secret_envelope from 1 for 4) = 'NXE1'::bytea),
    min_refresh_seconds  integer NOT NULL DEFAULT 60 CHECK (min_refresh_seconds BETWEEN 1 AND 86400),
    policy_override      text NOT NULL DEFAULT 'given' CHECK (policy_override IN ('given', 'disabled', 'nxdomain', 'nodata', 'passthru', 'drop', 'tcp_only')),
    refresh_nonce        bigint NOT NULL DEFAULT 0,
    revision             bigint NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CHECK ((source_type = 'transfer' AND primary_address IS NOT NULL AND blob_sha256 IS NULL)
        OR (source_type = 'file' AND primary_address IS NULL AND tsig_algorithm IS NULL)),
    CHECK ((tsig_algorithm IS NULL) = (tsig_key_name IS NULL)),
    CHECK (tsig_secret_envelope IS NULL OR tsig_algorithm IS NOT NULL)
);
CREATE UNIQUE INDEX rpz_zones_position ON rpz_zones (position);

CREATE TABLE engine_rpz_status (
    engine_id       uuid NOT NULL REFERENCES engines (id) ON DELETE CASCADE,
    rpz_zone_id     uuid NOT NULL REFERENCES rpz_zones (id) ON DELETE CASCADE,
    serial          bigint NOT NULL,
    records         bigint NOT NULL,
    skipped         bigint NOT NULL,
    hits            bigint NOT NULL,
    last_success_at timestamptz,
    last_error      text NOT NULL DEFAULT '',
    stale           boolean NOT NULL DEFAULT false,
    reported_at     timestamptz NOT NULL,
    PRIMARY KEY (engine_id, rpz_zone_id)
);

-- +goose Down
DROP TABLE engine_rpz_status;
DROP TABLE rpz_zones;
