-- +goose Up
-- Account self-service: profile fields and the failed-attempt throttle.
ALTER TABLE users
    ADD COLUMN display_name text NOT NULL DEFAULT '' CHECK (length(display_name) <= 64),
    ADD COLUMN last_login_at timestamptz,
    ADD COLUMN preferences jsonb NOT NULL DEFAULT '{}';

-- One row per failed login or password change, keyed on the lower-cased username and the client
-- address; rows older than 15 minutes are deleted on every insert.
CREATE TABLE auth_failures (
    username text NOT NULL,
    client   text NOT NULL,
    at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX auth_failures_username_client_at ON auth_failures (username, client, at);

-- +goose Down
DROP TABLE auth_failures;
ALTER TABLE users DROP COLUMN preferences, DROP COLUMN last_login_at, DROP COLUMN display_name;
