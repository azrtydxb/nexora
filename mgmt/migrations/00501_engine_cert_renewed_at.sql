-- +goose Up
-- The last certificate issued to the engine over a control stream: the at-most-once-per-10-s renewal
-- limit is checked against it under the engine row lock, so reconnects and parallel streams share it.
ALTER TABLE engines ADD COLUMN cert_renewed_at timestamptz;

-- +goose Down
ALTER TABLE engines DROP COLUMN cert_renewed_at;
