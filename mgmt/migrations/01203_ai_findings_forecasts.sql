-- +goose Up
CREATE TABLE ai_findings (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         text NOT NULL CHECK (kind IN ('anomaly', 'insight')),
    candidate_id text NOT NULL,
    type         text NOT NULL,
    status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'acknowledged', 'dismissed', 'resolved')),
    severity     text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    confidence   real NOT NULL DEFAULT 0,
    title        text NOT NULL,
    description  text NOT NULL,
    detail       jsonb NOT NULL DEFAULT '{}',
    explained    boolean NOT NULL DEFAULT false,
    first_seen   timestamptz NOT NULL,
    last_seen    timestamptz NOT NULL,
    updated_by   text NOT NULL DEFAULT '',
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ai_findings_active ON ai_findings (kind, candidate_id) WHERE status IN ('open', 'acknowledged');
CREATE INDEX ai_findings_list ON ai_findings (kind, status, last_seen DESC);
CREATE TABLE ai_forecasts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         text NOT NULL CHECK (kind IN ('upstream', 'capacity')),
    subject      text NOT NULL,
    detail       jsonb NOT NULL,
    proposal_id  uuid,
    generated_at timestamptz NOT NULL,
    valid_until  timestamptz NOT NULL,
    UNIQUE (kind, subject, generated_at)
);
-- +goose Down
DROP TABLE ai_forecasts, ai_findings;
