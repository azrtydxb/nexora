-- +goose Up
CREATE TABLE dnssec_settings (
    singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    validation boolean NOT NULL DEFAULT true,
    validate_forwarded boolean NOT NULL DEFAULT true,
    rfc5011    boolean NOT NULL DEFAULT true,
    revision   bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO dnssec_settings DEFAULT VALUES;

CREATE TABLE trust_anchors (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zone       text NOT NULL,
    ds         text NOT NULL,
    source     text NOT NULL DEFAULT 'operator' CHECK (source IN ('iana', 'operator')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (zone, ds)
);
INSERT INTO trust_anchors (zone, ds, source) VALUES
    ('.', '20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D', 'iana'),
    ('.', '38696 8 2 683D2D0ACB8C9B712A1948B27F741219298D0A450D612C483AF444A4C0FB2B16', 'iana');

CREATE TABLE negative_trust_anchors (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain     text NOT NULL UNIQUE,
    reason     text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    expires_at timestamptz NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE engine_dnssec_status (
    engine_id   uuid PRIMARY KEY REFERENCES engines (id) ON DELETE CASCADE,
    stats       jsonb NOT NULL,
    reported_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE engine_dnssec_status;
DROP TABLE negative_trust_anchors;
DROP TABLE trust_anchors;
DROP TABLE dnssec_settings;
