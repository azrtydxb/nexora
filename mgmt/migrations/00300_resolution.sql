-- +goose Up
CREATE TABLE resolution_settings (
    singleton            boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    mode                 text NOT NULL DEFAULT 'forward' CHECK (mode IN ('forward', 'recursive')),
    qname_minimisation   boolean NOT NULL DEFAULT true,
    aggressive_nsec      boolean NOT NULL DEFAULT false,
    max_upstream_queries integer NOT NULL DEFAULT 100 CHECK (max_upstream_queries BETWEEN 1 AND 1000),
    max_delegation_depth integer NOT NULL DEFAULT 32 CHECK (max_delegation_depth BETWEEN 1 AND 64),
    authority_port       integer NOT NULL DEFAULT 53 CHECK (authority_port BETWEEN 1 AND 65535),
    root_hints           jsonb NOT NULL DEFAULT '[]'::jsonb,
    revision             bigint NOT NULL DEFAULT 1,
    updated_at           timestamptz NOT NULL DEFAULT now()
);
INSERT INTO resolution_settings DEFAULT VALUES;

CREATE TABLE forward_zones (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain     text NOT NULL UNIQUE,
    addresses  text[] NOT NULL CHECK (cardinality(addresses) BETWEEN 1 AND 16),
    validate   boolean NOT NULL DEFAULT false,
    revision   bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE forward_zones;
DROP TABLE resolution_settings;
