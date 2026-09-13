-- +goose Up
-- PKCS#11 signing keys leave the token only after the transaction that removed their rows
-- committed: the transaction queues them in dnssec_key_destruction and the queue is processed
-- after commit (retried until the token destroy succeeds). Token keys that no committed row
-- references (their generating transaction rolled back, or the process died) are recorded in
-- dnssec_token_orphans when a sweep first sees them and destroyed once a grace period passed.
CREATE TABLE dnssec_key_destruction (
    key_ref     bytea PRIMARY KEY,
    enqueued_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE dnssec_token_orphans (
    key_ref       bytea PRIMARY KEY,
    first_seen_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE dnssec_token_orphans;
DROP TABLE dnssec_key_destruction;
