-- +goose Up
-- Online DNSSEC signing of primary zones: per-zone settings, signing keys (private keys sealed
-- under the KEK or held in the PKCS#11 token) and the signature cache the signer reuses.
CREATE TABLE zone_dnssec (
    zone_id                   uuid PRIMARY KEY REFERENCES zones(id) ON DELETE CASCADE,
    enabled                   boolean NOT NULL DEFAULT false,
    algorithm                 smallint NOT NULL DEFAULT 13 CHECK (algorithm IN (8, 13)),
    nsec_mode                 text NOT NULL DEFAULT 'nsec3' CHECK (nsec_mode IN ('nsec', 'nsec3')),
    key_backend               text NOT NULL CHECK (key_backend IN ('kek', 'pkcs11')),
    propagation_delay_seconds integer NOT NULL DEFAULT 3600 CHECK (propagation_delay_seconds BETWEEN 1 AND 604800),
    parent_ds_ttl_seconds     integer NOT NULL DEFAULT 86400 CHECK (parent_ds_ttl_seconds BETWEEN 1 AND 604800),
    zsk_lifetime_days         integer NOT NULL DEFAULT 90 CHECK (zsk_lifetime_days BETWEEN 0 AND 3650),
    next_maintenance_at       timestamptz
);
CREATE INDEX zone_dnssec_due ON zone_dnssec (next_maintenance_at) WHERE enabled;

CREATE TABLE dnssec_keys (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id          uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    role             text NOT NULL CHECK (role IN ('ksk', 'zsk')),
    algorithm        smallint NOT NULL CHECK (algorithm IN (8, 13)),
    key_tag          integer NOT NULL,
    public_key       text NOT NULL,
    backend          text NOT NULL CHECK (backend IN ('kek', 'pkcs11')),
    key_ref          bytea NOT NULL UNIQUE,
    private_envelope bytea,
    state            text NOT NULL CHECK (state IN ('published', 'active', 'retired', 'removed')),
    ds_state         text NOT NULL DEFAULT 'none' CHECK (ds_state IN ('none', 'pending', 'seen')),
    published_at     timestamptz NOT NULL DEFAULT now(),
    activated_at     timestamptz,
    retired_at       timestamptz,
    removed_at       timestamptz,
    ds_seen_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (state = 'removed' OR (backend = 'kek') = (private_envelope IS NOT NULL))
);
CREATE INDEX dnssec_keys_zone ON dnssec_keys (zone_id, state);

CREATE TABLE zone_signatures (
    zone_id      uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    owner        text NOT NULL,
    type_covered integer NOT NULL,
    key_tag      integer NOT NULL,
    rrset_digest bytea NOT NULL,
    expiration   timestamptz NOT NULL,
    rrsig        bytea NOT NULL,
    PRIMARY KEY (zone_id, owner, type_covered, key_tag)
);

-- +goose Down
DROP TABLE zone_signatures;
DROP TABLE dnssec_keys;
DROP TABLE zone_dnssec;
