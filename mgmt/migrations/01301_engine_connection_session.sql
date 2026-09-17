-- +goose Up
-- Stream identity fences delayed writers across instances and same-instance reconnects.
ALTER TABLE engines ADD COLUMN connection_session uuid;

-- +goose Down
ALTER TABLE engines DROP COLUMN connection_session;
