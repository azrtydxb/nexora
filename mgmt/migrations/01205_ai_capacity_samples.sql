-- +goose Up
CREATE TABLE ai_capacity_samples (
    day         date NOT NULL,
    resource    text NOT NULL,
    value       double precision NOT NULL,
    limit_value double precision,
    PRIMARY KEY (day, resource)
);
-- +goose Down
DROP TABLE ai_capacity_samples;
