-- +goose Up
CREATE TABLE engine_groups (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                  text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    description           text NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
    upstream_mode         text NOT NULL DEFAULT 'inherit' CHECK (upstream_mode IN ('inherit', 'override')),
    extra_acl_cidrs       cidr[] NOT NULL DEFAULT '{}',
    otlp_endpoint         text NOT NULL DEFAULT '',
    rollout_strategy      text NOT NULL DEFAULT 'all_at_once' CHECK (rollout_strategy IN ('all_at_once', 'canary')),
    canary_count          integer NOT NULL DEFAULT 0 CHECK (canary_count >= 0),
    canary_percent        integer NOT NULL DEFAULT 0 CHECK (canary_percent BETWEEN 0 AND 100),
    ack_timeout_seconds   integer NOT NULL DEFAULT 60 CHECK (ack_timeout_seconds BETWEEN 5 AND 3600),
    health_window_seconds integer NOT NULL DEFAULT 30 CHECK (health_window_seconds BETWEEN 20 AND 3600),
    max_servfail_ratio    double precision NOT NULL DEFAULT 0.05 CHECK (max_servfail_ratio >= 0 AND max_servfail_ratio <= 1),
    min_health_queries    integer NOT NULL DEFAULT 100 CHECK (min_health_queries >= 0),
    rollouts_paused       boolean NOT NULL DEFAULT false,
    stable_version        bigint,
    revision              bigint NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT engine_groups_canary_size CHECK (rollout_strategy = 'all_at_once' OR canary_count > 0 OR canary_percent > 0)
);
INSERT INTO engine_groups (id, name, description)
VALUES ('00000000-0000-0000-0000-000000000001', 'default', 'Engines not assigned to another engine group');

ALTER TABLE engines
    ADD COLUMN engine_group_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
        REFERENCES engine_groups (id) ON DELETE RESTRICT,
    ADD COLUMN labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(labels) = 'object'),
    ADD COLUMN revoked_at timestamptz,
    ADD COLUMN cert_rotate_requested_at timestamptz,
    ADD COLUMN revision bigint NOT NULL DEFAULT 1;
CREATE INDEX engines_engine_group ON engines (engine_group_id);

ALTER TABLE join_tokens
    ADD COLUMN engine_group_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
        REFERENCES engine_groups (id) ON DELETE CASCADE,
    ADD COLUMN labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(labels) = 'object'),
    ADD COLUMN max_uses integer CHECK (max_uses IS NULL OR max_uses >= 1);

ALTER TABLE upstreams     ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
ALTER TABLE filter_lists  ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
ALTER TABLE policy_groups ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
ALTER TABLE forward_zones ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
ALTER TABLE zones         ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
ALTER TABLE rpz_zones     ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
-- Only global rewrites have their own engine group; policy group rewrites follow their policy group.
ALTER TABLE rewrites
    ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT,
    ADD CONSTRAINT rewrites_engine_group_global_only CHECK (group_id IS NULL OR engine_group_id IS NULL);
CREATE INDEX upstreams_engine_group ON upstreams (engine_group_id);
CREATE INDEX filter_lists_engine_group ON filter_lists (engine_group_id);
CREATE INDEX policy_groups_engine_group ON policy_groups (engine_group_id);
CREATE INDEX rewrites_engine_group ON rewrites (engine_group_id);
CREATE INDEX forward_zones_engine_group ON forward_zones (engine_group_id);
CREATE INDEX zones_engine_group ON zones (engine_group_id);
CREATE INDEX rpz_zones_engine_group ON rpz_zones (engine_group_id);

ALTER TABLE config_versions ALTER COLUMN snapshot DROP NOT NULL;

CREATE TABLE group_snapshots (
    version         bigint NOT NULL REFERENCES config_versions (version) ON DELETE CASCADE,
    engine_group_id uuid NOT NULL REFERENCES engine_groups (id) ON DELETE CASCADE,
    snapshot        bytea NOT NULL,
    content_sha256  text NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (version, engine_group_id)
);
CREATE INDEX group_snapshots_group_version ON group_snapshots (engine_group_id, version DESC);

CREATE TABLE rollouts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    engine_group_id   uuid NOT NULL REFERENCES engine_groups (id) ON DELETE CASCADE,
    version           bigint NOT NULL,
    from_version      bigint,
    kind              text NOT NULL CHECK (kind IN ('change', 'rollback', 'republish')),
    strategy          text NOT NULL CHECK (strategy IN ('all_at_once', 'canary')),
    state             text NOT NULL CHECK (state IN
                        ('pending', 'canary', 'verifying', 'rolling', 'completed', 'halted', 'rolled_back', 'superseded')),
    params            jsonb NOT NULL,
    canary_engine_ids uuid[] NOT NULL DEFAULT '{}',
    phase_started_at  timestamptz,
    halt_reason       text NOT NULL DEFAULT '',
    created_by        text NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz,
    FOREIGN KEY (version, engine_group_id) REFERENCES group_snapshots (version, engine_group_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX rollouts_one_active_per_group ON rollouts (engine_group_id)
    WHERE state IN ('canary', 'verifying', 'rolling');
CREATE INDEX rollouts_group_version ON rollouts (engine_group_id, version DESC);
CREATE INDEX rollouts_open ON rollouts (created_at) WHERE state IN ('pending', 'canary', 'verifying', 'rolling');

-- The newest pre-M5 version becomes the default group's completed, stable snapshot.
INSERT INTO group_snapshots (version, engine_group_id, snapshot, content_sha256)
SELECT version, '00000000-0000-0000-0000-000000000001', snapshot, encode(sha256(snapshot), 'hex')
FROM config_versions WHERE snapshot IS NOT NULL ORDER BY version DESC LIMIT 1;
INSERT INTO rollouts (engine_group_id, version, kind, strategy, state, params, created_by, phase_started_at, finished_at)
SELECT engine_group_id, version, 'change', 'all_at_once', 'completed', '{"strategy":"all_at_once"}'::jsonb, 'migration', now(), now()
FROM group_snapshots;
UPDATE engine_groups SET stable_version = (SELECT max(version) FROM group_snapshots)
WHERE id = '00000000-0000-0000-0000-000000000001';

CREATE TABLE engine_certificates (
    serial        text PRIMARY KEY CHECK (serial ~ '^[0-9a-f]+$'),
    engine_id     uuid NOT NULL REFERENCES engines (id) ON DELETE CASCADE,
    not_before    timestamptz NOT NULL,
    not_after     timestamptz NOT NULL,
    issued_at     timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    revoke_reason text CHECK (revoke_reason IN ('revoked', 'superseded')),
    CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL))
);
CREATE INDEX engine_certificates_engine ON engine_certificates (engine_id, issued_at DESC);
-- M1 issued every engine certificate with NotBefore now-1h and NotAfter now+365d at enrollment.
INSERT INTO engine_certificates (serial, engine_id, not_before, not_after, issued_at, revoked_at, revoke_reason)
SELECT lower(certificate_serial), id, enrolled_at - interval '1 hour', enrolled_at + interval '365 days', enrolled_at,
       deleted_at, CASE WHEN deleted_at IS NOT NULL THEN 'revoked' END
FROM engines WHERE certificate_serial ~ '^[0-9a-fA-F]+$'
ON CONFLICT (serial) DO NOTHING;

-- +goose Down
DROP TABLE engine_certificates;
DROP TABLE rollouts;
DROP TABLE group_snapshots;
DELETE FROM config_versions WHERE snapshot IS NULL;
ALTER TABLE config_versions ALTER COLUMN snapshot SET NOT NULL;
ALTER TABLE rewrites DROP CONSTRAINT rewrites_engine_group_global_only, DROP COLUMN engine_group_id;
ALTER TABLE rpz_zones DROP COLUMN engine_group_id;
ALTER TABLE zones DROP COLUMN engine_group_id;
ALTER TABLE forward_zones DROP COLUMN engine_group_id;
ALTER TABLE policy_groups DROP COLUMN engine_group_id;
ALTER TABLE filter_lists DROP COLUMN engine_group_id;
ALTER TABLE upstreams DROP COLUMN engine_group_id;
ALTER TABLE join_tokens DROP COLUMN max_uses, DROP COLUMN labels, DROP COLUMN engine_group_id;
ALTER TABLE engines DROP COLUMN revision, DROP COLUMN cert_rotate_requested_at, DROP COLUMN revoked_at,
    DROP COLUMN labels, DROP COLUMN engine_group_id;
DROP TABLE engine_groups;
