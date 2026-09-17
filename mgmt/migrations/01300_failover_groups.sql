-- +goose Up
-- Availability membership is independent of policy engine_groups. No existing
-- engines, policies, VIPs or snapshots are changed by this migration.
CREATE TABLE failover_groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    frontend_ip inet NOT NULL UNIQUE CHECK (
        family(frontend_ip) = 4 AND masklen(frontend_ip) = 32
        AND NOT frontend_ip <<= '0.0.0.0/8'::inet
        AND NOT frontend_ip <<= '127.0.0.0/8'::inet
        AND NOT frontend_ip <<= '169.254.0.0/16'::inet
        AND NOT frontend_ip <<= '224.0.0.0/3'::inet),
    member_a uuid NOT NULL REFERENCES engines(id) ON DELETE RESTRICT,
    member_b uuid NOT NULL REFERENCES engines(id) ON DELETE RESTRICT,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (member_a <> member_b)
);
-- Two slots cap membership at two. Deferred reverse references below require
-- BOTH members to exist at commit. engine_id uniqueness spans both slots and
-- all groups, including concurrent transactions (no check-then-insert race).
CREATE TABLE failover_members (
    group_id uuid NOT NULL REFERENCES failover_groups(id) ON DELETE CASCADE,
    engine_id uuid NOT NULL UNIQUE REFERENCES engines(id) ON DELETE RESTRICT,
    slot smallint NOT NULL CHECK (slot IN (1, 2)),
    PRIMARY KEY (group_id, engine_id),
    UNIQUE (group_id, slot)
);
ALTER TABLE failover_groups
    ADD CONSTRAINT failover_member_a FOREIGN KEY (id, member_a)
        REFERENCES failover_members(group_id, engine_id) DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT failover_member_b FOREIGN KEY (id, member_b)
        REFERENCES failover_members(group_id, engine_id) DEFERRABLE INITIALLY DEFERRED;

-- No observation means UNKNOWN, never healthy. This is not an election lease
-- and does not fence an advertiser. Only the future fenced adapter may write it.
CREATE TABLE failover_observations (
    group_id uuid PRIMARY KEY REFERENCES failover_groups(id) ON DELETE CASCADE,
    applied_generation bigint NOT NULL CHECK (applied_generation > 0),
    owner text NOT NULL CHECK (length(owner) BETWEEN 1 AND 253),
    state text NOT NULL CHECK (state IN ('healthy', 'degraded', 'unavailable')),
    reason text NOT NULL CHECK (length(reason) <= 1024),
    observed_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE failover_observations;
ALTER TABLE failover_groups DROP CONSTRAINT failover_member_a, DROP CONSTRAINT failover_member_b;
DROP TABLE failover_members;
DROP TABLE failover_groups;
