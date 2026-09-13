-- +goose Up
-- A refresh requested by NOTIFY, the API or zone creation: the trigger the scheduler reports for
-- the next run, and a counter that tells a running refresh that another request arrived meanwhile.
ALTER TABLE zones
    ADD COLUMN refresh_trigger  text NOT NULL DEFAULT '' CHECK (refresh_trigger IN ('', 'notify', 'manual', 'create')),
    ADD COLUMN refresh_requests bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE zones DROP COLUMN refresh_requests, DROP COLUMN refresh_trigger;
