package control

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func supersededConnection() error {
	return status.Error(codes.Aborted, "engine connection superseded")
}

// writeConnection performs a single engine update whose WHERE clause includes the
// stream's session token. PostgreSQL rechecks it after waiting for a concurrent claim.
func (s *Server) writeConnection(ctx context.Context, query string, args ...any) error {
	tag, err := s.st.Pool.Exec(ctx, query, args...)
	if err == nil && tag.RowsAffected() == 0 {
		return supersededConnection()
	}
	return err
}

// withConnection fences multi-statement state writes, including synchronous stats
// callbacks, against reconnects. Callbacks must use the supplied transaction for
// all persistence and finish before returning; acquiring another pool connection
// can deadlock when every pool slot is occupied by an ownership transaction.
func (s *Server) withConnection(ctx context.Context, sub *subscriber, write func(pgx.Tx) error) error {
	return s.st.InTx(ctx, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `select id::text from engines
   where id = $1 and connection_session = $2 for no key update`, sub.id, sub.sessionID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return supersededConnection()
		}
		if err != nil {
			return err
		}
		return write(tx)
	})
}
