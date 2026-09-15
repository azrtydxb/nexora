-- +goose Up
CREATE TABLE ai_agent_runs (
    id            bigserial PRIMARY KEY,
    agent         text NOT NULL,
    instance_id   text NOT NULL,
    started_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    outcome       text NOT NULL DEFAULT 'running',
    input_tokens  bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    detail        jsonb NOT NULL DEFAULT '{}',
    error         text NOT NULL DEFAULT ''
);
CREATE INDEX ai_agent_runs_agent ON ai_agent_runs (agent, started_at DESC);
CREATE TABLE ai_agent_requests (
    agent        text PRIMARY KEY,
    requested_at timestamptz NOT NULL DEFAULT now(),
    requested_by text NOT NULL
);
CREATE TABLE ai_tasks (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           text NOT NULL,
    status         text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    requested_by   text NOT NULL,
    requester_kind text NOT NULL,
    instance_id    text NOT NULL,
    input          jsonb NOT NULL,
    result         jsonb,
    error_code     text NOT NULL DEFAULT '',
    error_message  text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    finished_at    timestamptz
);
CREATE INDEX ai_tasks_created ON ai_tasks (created_at);
-- +goose Down
DROP TABLE ai_tasks, ai_agent_requests, ai_agent_runs;
