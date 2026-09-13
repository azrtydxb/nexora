package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EngineTLSState records which DNS serving certificate an engine accepted. It holds no key material.
type EngineTLSState struct {
	EngineID    uuid.UUID
	Fingerprint string
	Applied     bool
	Error       string
	UpdatedAt   time.Time
}

// UpsertEngineTLSState stores the engine's latest TlsMaterialResult.
func UpsertEngineTLSState(ctx context.Context, q PolicyQuerier, s EngineTLSState) error {
	_, err := q.Exec(ctx, `insert into engine_tls_state(engine_id, fingerprint, applied, error) values ($1, $2, $3, $4)
		on conflict (engine_id) do update set fingerprint = excluded.fingerprint, applied = excluded.applied,
		error = excluded.error, updated_at = now()`, s.EngineID, s.Fingerprint, s.Applied, s.Error)
	return MapError(err)
}

// ListEngineTLSState returns every engine's TLS state ordered by engine id.
func ListEngineTLSState(ctx context.Context, q PolicyQuerier) ([]EngineTLSState, error) {
	rows, err := q.Query(ctx, "select engine_id, fingerprint, applied, error, updated_at from engine_tls_state order by engine_id")
	if err != nil {
		return nil, MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (EngineTLSState, error) {
		var s EngineTLSState
		err := row.Scan(&s.EngineID, &s.Fingerprint, &s.Applied, &s.Error, &s.UpdatedAt)
		return s, err
	})
	return out, MapError(err)
}
