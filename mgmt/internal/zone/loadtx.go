package zone

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LoadZoneTx returns zone id read inside tx, locking its row when forUpdate (store.ErrNotFound
// when it does not exist). Callers that rebuild a zone in their own transaction use it.
func LoadZoneTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, forUpdate bool) (*Zone, error) {
	return loadZone(ctx, tx, id, forUpdate)
}
