-- +goose Up
CREATE TABLE tsig_keys (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text NOT NULL UNIQUE CHECK (name ~ '^([a-z0-9_-]{1,63}\.)+$'),
    algorithm       text NOT NULL CHECK (algorithm IN ('hmac-sha256', 'hmac-sha384', 'hmac-sha512')),
    -- NXE1 envelope from internal/secrets; never plaintext.
    secret_envelope bytea NOT NULL CHECK (substring(secret_envelope from 1 for 4) = 'NXE1'::bytea),
    revision        bigint NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE zones (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 text NOT NULL UNIQUE CHECK (name ~ '\.$' AND name <> '.'),
    kind                 text NOT NULL CHECK (kind IN ('primary', 'secondary')),
    revision             bigint NOT NULL DEFAULT 1,
    serial               bigint NOT NULL DEFAULT 1 CHECK (serial BETWEEN 0 AND 4294967295),
    default_ttl          integer NOT NULL DEFAULT 3600 CHECK (default_ttl >= 0),
    soa_mname            text NOT NULL,
    soa_rname            text NOT NULL,
    soa_refresh          integer NOT NULL DEFAULT 10800 CHECK (soa_refresh > 0),
    soa_retry            integer NOT NULL DEFAULT 3600 CHECK (soa_retry > 0),
    soa_expire           integer NOT NULL DEFAULT 1209600 CHECK (soa_expire > 0),
    soa_minimum          integer NOT NULL DEFAULT 3600 CHECK (soa_minimum >= 0),
    soa_ttl              integer NOT NULL DEFAULT 3600 CHECK (soa_ttl >= 0),
    transfer_allow_cidrs cidr[] NOT NULL DEFAULT '{}',
    transfer_tsig_key_id uuid REFERENCES tsig_keys(id),
    notify_targets       jsonb NOT NULL DEFAULT '[]',
    update_tsig_key_ids  uuid[] NOT NULL DEFAULT '{}',
    primaries            jsonb NOT NULL DEFAULT '[]',
    current_seq          bigint NOT NULL DEFAULT 0,
    image_seq            bigint NOT NULL DEFAULT 0,
    loaded               boolean NOT NULL DEFAULT false,
    last_refresh_at      timestamptz,
    last_success_at      timestamptz,
    next_refresh_at      timestamptz,
    expires_at           timestamptz,
    expired              boolean NOT NULL DEFAULT false,
    last_error           text NOT NULL DEFAULT '',
    last_trigger         text NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE zone_records (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id    uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    owner      text NOT NULL,
    rtype      integer NOT NULL CHECK (rtype BETWEEN 1 AND 65535),
    ttl        integer NOT NULL CHECK (ttl >= 0),
    rdata      text NOT NULL,
    rdata_wire bytea NOT NULL,
    revision   bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX zone_records_rr ON zone_records (zone_id, lower(owner), rtype, sha256(rdata_wire));
CREATE INDEX zone_records_owner ON zone_records (zone_id, lower(owner));

-- seq (monotonic per zone) orders images and journal entries because serials wrap.
CREATE TABLE zone_images (
    zone_id     uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    serial      bigint NOT NULL,
    blob_sha256 text NOT NULL REFERENCES blobs(sha256),
    raw_size    bigint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (zone_id, seq)
);

CREATE TABLE zone_journal (
    zone_id     uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    from_serial bigint NOT NULL,
    to_serial   bigint NOT NULL,
    blob_sha256 text NOT NULL REFERENCES blobs(sha256),
    raw_size    bigint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (zone_id, seq)
);

-- +goose Down
DROP TABLE zone_journal;
DROP TABLE zone_images;
DROP TABLE zone_records;
DROP TABLE zones;
DROP TABLE tsig_keys;
