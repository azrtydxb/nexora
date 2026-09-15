-- +goose Up
CREATE TABLE ai_proposals (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source         text NOT NULL CHECK (source IN ('filter_recommendations', 'config_assistant', 'upstream_prediction',
                                                   'rollout_risk', 'capacity_forecast', 'rpz_suggestions')),
    fingerprint    text NOT NULL,
    status         text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'applied', 'failed', 'stale', 'dismissed', 'superseded')),
    title          text NOT NULL,
    description    text NOT NULL,
    priority       text NOT NULL CHECK (priority IN ('low', 'medium', 'high')),
    impact         jsonb NOT NULL DEFAULT '{}',
    evidence       jsonb NOT NULL DEFAULT '{}',
    actions        jsonb NOT NULL,
    risk           jsonb,
    session_id     uuid,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    reviewed_by    text NOT NULL DEFAULT '',
    reviewed_at    timestamptz,
    result         jsonb,
    dismiss_reason text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX ai_proposals_open_fingerprint ON ai_proposals (fingerprint) WHERE status = 'open';
CREATE INDEX ai_proposals_status ON ai_proposals (status, created_at DESC);
CREATE TABLE ai_rpz_rules (
    record      text PRIMARY KEY,
    policy      text NOT NULL,
    category    text NOT NULL,
    reason      text NOT NULL,
    proposal_id uuid NOT NULL,
    applied_by  text NOT NULL,
    applied_at  timestamptz NOT NULL DEFAULT now()
);
-- +goose Down
DROP TABLE ai_rpz_rules, ai_proposals;
