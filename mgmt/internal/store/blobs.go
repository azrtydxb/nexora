package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jackc/pgx/v5"
)

// PutBlob stores data content-addressed by its SHA-256 (hex) and returns the digest and size.
func PutBlob(ctx context.Context, tx pgx.Tx, data []byte) (string, int64, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if _, err := tx.Exec(ctx, "insert into blobs(sha256, size, data) values ($1, $2, $3) on conflict do nothing", sha, len(data), data); err != nil {
		return "", 0, MapError(err)
	}
	return sha, int64(len(data)), nil
}
