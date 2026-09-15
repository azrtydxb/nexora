-- +goose Up
CREATE TABLE ai_rollout_risks (
    rollout_id  uuid PRIMARY KEY REFERENCES rollouts (id) ON DELETE CASCADE,
    status      text NOT NULL CHECK (status IN ('pending', 'assessed', 'failed', 'skipped')),
    risk_score  integer,
    risk_level  text,
    analysis    text NOT NULL DEFAULT '',
    detail      jsonb NOT NULL DEFAULT '{}',
    proposal_id uuid,
    assessed_at timestamptz,
    error       text NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE ai_rollout_risks;
