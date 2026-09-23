-- +goose Up
CREATE TABLE catalog_zones (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id          uuid NOT NULL UNIQUE REFERENCES zones (id) ON DELETE CASCADE,
    role             text NOT NULL CHECK (role IN ('producer', 'consumer')),
    broken_reason    text NOT NULL DEFAULT '',
    processed_serial bigint,
    processed_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE zones
    ADD COLUMN catalog_zone_id      uuid REFERENCES catalog_zones (id) ON DELETE SET NULL,
    ADD COLUMN catalog_member_label text NOT NULL DEFAULT '';
CREATE INDEX zones_catalog_zone ON zones (catalog_zone_id) WHERE catalog_zone_id IS NOT NULL;
CREATE TABLE catalog_member_issues (
    catalog_zone_id uuid NOT NULL REFERENCES catalog_zones (id) ON DELETE CASCADE,
    member_name     text NOT NULL,
    label           text NOT NULL,
    issue           text NOT NULL,
    seen_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (catalog_zone_id, member_name)
);

-- +goose Down
DROP TABLE catalog_member_issues;
DROP INDEX zones_catalog_zone;
ALTER TABLE zones DROP COLUMN catalog_member_label, DROP COLUMN catalog_zone_id;
DROP TABLE catalog_zones;
