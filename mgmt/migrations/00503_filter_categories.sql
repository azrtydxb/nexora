-- +goose Up
-- Filter categories: the embedded catalog (mgmt/internal/catalog/catalog.yaml) synced into rows.
CREATE TABLE filter_categories (
    key        text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9-]{0,31}$'),
    enabled    boolean NOT NULL DEFAULT false,
    revision   bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row: the digest of the catalog last synced, so an unchanged catalog publishes nothing.
CREATE TABLE filter_catalog_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    digest    text NOT NULL DEFAULT ''
);
INSERT INTO filter_catalog_state DEFAULT VALUES;

ALTER TABLE filter_lists
    ADD COLUMN category_key text REFERENCES filter_categories (key) ON DELETE CASCADE,
    ADD COLUMN source_key text,
    ADD COLUMN managed_by_catalog boolean NOT NULL DEFAULT false,
    ADD COLUMN archive_member text NOT NULL DEFAULT '',
    ADD COLUMN catalog_position integer,
    ADD COLUMN license_acknowledged_at timestamptz,
    ADD CONSTRAINT filter_lists_catalog_columns CHECK (
        managed_by_catalog = (category_key IS NOT NULL AND source_key IS NOT NULL AND catalog_position IS NOT NULL)
        AND (managed_by_catalog OR archive_member = '')
        AND (NOT managed_by_catalog OR (kind = 'block' AND engine_group_id IS NULL))
    );
CREATE UNIQUE INDEX filter_lists_catalog_source ON filter_lists (category_key, source_key) WHERE managed_by_catalog;

ALTER TABLE policy_groups ADD COLUMN category_keys text[] NOT NULL DEFAULT '{}';

ALTER TABLE engine_groups ADD COLUMN filter_index_max_bytes bigint NOT NULL DEFAULT 0
    CHECK (filter_index_max_bytes = 0 OR filter_index_max_bytes >= 16777216);

-- +goose Down
ALTER TABLE engine_groups DROP COLUMN filter_index_max_bytes;
ALTER TABLE policy_groups DROP COLUMN category_keys;
DELETE FROM filter_lists WHERE managed_by_catalog;
DROP INDEX filter_lists_catalog_source;
ALTER TABLE filter_lists
    DROP CONSTRAINT filter_lists_catalog_columns,
    DROP COLUMN license_acknowledged_at,
    DROP COLUMN catalog_position,
    DROP COLUMN archive_member,
    DROP COLUMN managed_by_catalog,
    DROP COLUMN source_key,
    DROP COLUMN category_key;
DROP TABLE filter_catalog_state;
DROP TABLE filter_categories;
