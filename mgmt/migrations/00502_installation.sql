-- +goose Up
-- One row: this installation's id. PKCS#11 DNSSEC key objects carry it in their label, so orphan
-- sweeps of installations sharing a token never destroy each other's keys.
CREATE TABLE installation (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    singleton boolean NOT NULL DEFAULT true UNIQUE CHECK (singleton)
);
INSERT INTO installation DEFAULT VALUES;

-- +goose Down
DROP TABLE installation;
