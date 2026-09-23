-- +goose Up
ALTER TABLE engine_groups
    ADD COLUMN mdns_enabled            boolean NOT NULL DEFAULT false,
    ADD COLUMN mdns_interfaces         text[] NOT NULL DEFAULT '{}',
    ADD COLUMN mdns_timeout_ms         integer NOT NULL DEFAULT 500 CHECK (mdns_timeout_ms BETWEEN 100 AND 5000),
    ADD COLUMN mdns_reflect            boolean NOT NULL DEFAULT false,
    ADD COLUMN mdns_reflect_interfaces text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT engine_groups_mdns_interfaces CHECK (NOT mdns_enabled OR cardinality(mdns_interfaces) >= 1),
    ADD CONSTRAINT engine_groups_mdns_reflect CHECK (NOT mdns_reflect OR cardinality(mdns_reflect_interfaces) >= 2);

-- +goose Down
ALTER TABLE engine_groups DROP CONSTRAINT engine_groups_mdns_reflect, DROP CONSTRAINT engine_groups_mdns_interfaces,
    DROP COLUMN mdns_reflect_interfaces, DROP COLUMN mdns_reflect, DROP COLUMN mdns_timeout_ms,
    DROP COLUMN mdns_interfaces, DROP COLUMN mdns_enabled;
