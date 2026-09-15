-- +goose Up
CREATE TABLE ai_domain_verdicts (
    name       text PRIMARY KEY,
    is_threat  boolean NOT NULL,
    categories text[] NOT NULL DEFAULT '{}',
    confidence real NOT NULL,
    reasoning  text NOT NULL DEFAULT '',
    checked_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE TABLE ai_list_classifications (
    list_id       uuid PRIMARY KEY REFERENCES filter_lists (id) ON DELETE CASCADE,
    blob_sha256   text NOT NULL,
    sample_size   integer NOT NULL,
    breakdown     jsonb NOT NULL,
    classified_at timestamptz NOT NULL
);
-- +goose Down
DROP TABLE ai_list_classifications, ai_domain_verdicts;
