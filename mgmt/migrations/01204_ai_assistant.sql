-- +goose Up
CREATE TABLE ai_assistant_sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   text NOT NULL,
    owner_kind text NOT NULL,
    title      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ai_assistant_messages (
    id          bigserial PRIMARY KEY,
    session_id  uuid NOT NULL REFERENCES ai_assistant_sessions (id) ON DELETE CASCADE,
    role        text NOT NULL CHECK (role IN ('user', 'assistant')),
    content     text NOT NULL,
    proposal_id uuid,
    task_id     uuid,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_assistant_messages_session ON ai_assistant_messages (session_id, id);
-- +goose Down
DROP TABLE ai_assistant_messages, ai_assistant_sessions;
