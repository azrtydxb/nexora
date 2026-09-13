-- +goose Up
CREATE TABLE policy_groups (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                   text NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9 _.-]{0,62}$'),
    description            text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    safe_search_google     boolean NOT NULL DEFAULT false,
    safe_search_bing       boolean NOT NULL DEFAULT false,
    safe_search_duckduckgo boolean NOT NULL DEFAULT false,
    safe_search_youtube    text NOT NULL DEFAULT 'off' CHECK (safe_search_youtube IN ('off', 'moderate', 'strict')),
    revision               bigint NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

-- An exact prefix belongs to at most one group; overlapping prefixes are allowed (most specific wins).
CREATE TABLE policy_group_cidrs (
    cidr     cidr PRIMARY KEY,
    group_id uuid NOT NULL REFERENCES policy_groups (id) ON DELETE CASCADE
);
CREATE INDEX policy_group_cidrs_group ON policy_group_cidrs (group_id);

CREATE TABLE policy_group_filter_lists (
    group_id       uuid NOT NULL REFERENCES policy_groups (id) ON DELETE CASCADE,
    filter_list_id uuid NOT NULL REFERENCES filter_lists (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, filter_list_id)
);

CREATE TABLE policy_group_allowlist (
    group_id uuid NOT NULL REFERENCES policy_groups (id) ON DELETE CASCADE,
    domain   text NOT NULL CHECK (domain ~ '^([a-z0-9_]([a-z0-9_-]{0,61}(?:[a-z0-9_]))?\.)*[a-z0-9_]([a-z0-9_-]{0,61}(?:[a-z0-9_]))?$' AND length(domain) <= 253),
    PRIMARY KEY (group_id, domain)
);

CREATE TABLE rewrites (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id   uuid REFERENCES policy_groups (id) ON DELETE CASCADE, -- NULL = global
    name       text NOT NULL CHECK (name ~ '^(\*\.)?([a-z0-9_]([a-z0-9_-]{0,61}(?:[a-z0-9_]))?\.)*[a-z0-9_]([a-z0-9_-]{0,61}(?:[a-z0-9_]))?$' AND length(name) <= 253),
    type       text NOT NULL CHECK (type IN ('A', 'AAAA', 'CNAME')),
    value      text NOT NULL CHECK (length(value) BETWEEN 1 AND 253),
    ttl        integer NOT NULL DEFAULT 300 CHECK (ttl BETWEEN 0 AND 86400),
    revision   bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX rewrites_unique_record
    ON rewrites (COALESCE(group_id, '00000000-0000-0000-0000-000000000000'::uuid), name, type, value);
CREATE INDEX rewrites_scope_name ON rewrites (group_id, name);

CREATE TABLE global_safe_search (
    singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    google     boolean NOT NULL DEFAULT false,
    bing       boolean NOT NULL DEFAULT false,
    duckduckgo boolean NOT NULL DEFAULT false,
    youtube    text NOT NULL DEFAULT 'off' CHECK (youtube IN ('off', 'moderate', 'strict')),
    revision   bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO global_safe_search DEFAULT VALUES;

-- Which DNS serving certificate each engine accepted. No key material is stored.
CREATE TABLE engine_tls_state (
    engine_id   uuid PRIMARY KEY REFERENCES engines (id) ON DELETE CASCADE,
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    applied     boolean NOT NULL,
    error       text NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE engine_tls_state;
DROP TABLE global_safe_search;
DROP TABLE rewrites;
DROP TABLE policy_group_allowlist;
DROP TABLE policy_group_filter_lists;
DROP TABLE policy_group_cidrs;
DROP TABLE policy_groups;
