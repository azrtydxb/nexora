-- +goose Up
ALTER TABLE zones
    ADD COLUMN zonemd_generate boolean NOT NULL DEFAULT false,
    ADD COLUMN zonemd_verify   text NOT NULL DEFAULT 'if_present' CHECK (zonemd_verify IN ('off', 'if_present', 'required')),
    ADD COLUMN zonemd_status   text NOT NULL DEFAULT 'not_checked' CHECK (zonemd_status IN ('not_checked', 'off', 'absent', 'verified', 'failed')),
    ADD COLUMN zonemd_error    text NOT NULL DEFAULT '',
    ADD CONSTRAINT zones_zonemd_generate_primary CHECK (kind = 'primary' OR NOT zonemd_generate);
ALTER TABLE rpz_zones
    ADD COLUMN zonemd_verify text NOT NULL DEFAULT 'if_present' CHECK (zonemd_verify IN ('off', 'if_present', 'required'));
-- Existing rows keep today's behaviour (no verification); new rows default to if_present
-- (lead decision 2026-09-15).
UPDATE zones SET zonemd_verify = 'off';
UPDATE rpz_zones SET zonemd_verify = 'off';
ALTER TABLE engine_rpz_status
    ADD COLUMN zonemd       text NOT NULL DEFAULT 'off' CHECK (zonemd IN ('off', 'absent', 'verified', 'failed')),
    ADD COLUMN zonemd_error text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE engine_rpz_status DROP COLUMN zonemd_error, DROP COLUMN zonemd;
ALTER TABLE rpz_zones DROP COLUMN zonemd_verify;
ALTER TABLE zones DROP CONSTRAINT zones_zonemd_generate_primary,
    DROP COLUMN zonemd_error, DROP COLUMN zonemd_status, DROP COLUMN zonemd_verify, DROP COLUMN zonemd_generate;
