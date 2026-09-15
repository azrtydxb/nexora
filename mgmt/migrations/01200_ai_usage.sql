-- +goose Up
CREATE TABLE ai_usage (
    day              date   NOT NULL,
    feature          text   NOT NULL,
    requests         bigint NOT NULL DEFAULT 0,
    input_tokens     bigint NOT NULL DEFAULT 0,
    output_tokens    bigint NOT NULL DEFAULT 0,
    reasoning_tokens bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (day, feature)
);
-- +goose Down
DROP TABLE ai_usage;
