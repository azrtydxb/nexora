-- +goose Up
-- M6: authoritative query access separate from recursion access. Existing installs keep
-- allow_cidrs as recursion access and answer hosted zones to everyone, as before.
ALTER TABLE access_control
    ADD COLUMN authoritative_allow_cidrs cidr[] NOT NULL DEFAULT array['0.0.0.0/0', '::/0']::cidr[];
ALTER TABLE zones
    ADD COLUMN allow_query_cidrs cidr[] NOT NULL DEFAULT '{}',
    ADD COLUMN update_allow_cidrs cidr[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE zones DROP COLUMN update_allow_cidrs, DROP COLUMN allow_query_cidrs;
ALTER TABLE access_control DROP COLUMN authoritative_allow_cidrs;
