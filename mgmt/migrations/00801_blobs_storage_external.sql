-- +goose Up
-- M7: blob data is zstd (incompressible); stored uncompressed out of line, a substring read
-- fetches only its chunk. Applies to rows written from now on.
ALTER TABLE blobs ALTER COLUMN data SET STORAGE EXTERNAL;

-- +goose Down
ALTER TABLE blobs ALTER COLUMN data SET STORAGE EXTENDED;
