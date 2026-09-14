-- +goose Up
-- Replies to engine log requests, handed from the instance holding the engine's stream to the
-- instance that serves the HTTP request. Transient: unlogged, deleted when read, pruned after 60 s.
CREATE UNLOGGED TABLE engine_log_replies (
    request_id uuid PRIMARY KEY,
    batch      bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE engine_log_replies;
