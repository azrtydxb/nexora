// Package forecast stores the upstream-health and capacity forecasts of the AI agents (M11 S-8, S-11).
package forecast

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Forecast is one stored forecast. Detail is the kind's forecast object (AiUpstreamPrediction or
// AiCapacityForecast in the API contract).
type Forecast struct {
	ID                      uuid.UUID
	Kind, Subject           string // kind upstream|capacity; subject upstream id or resource name
	Detail                  json.RawMessage
	ProposalID              *uuid.UUID
	GeneratedAt, ValidUntil time.Time
}

// Put stores f; a forecast with the same kind, subject and generated_at is replaced. A zero ID lets
// Put assign one.
func Put(ctx context.Context, st *store.Store, f Forecast) error {
	id := f.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	_, err := st.Pool.Exec(ctx, `insert into ai_forecasts(id, kind, subject, detail, proposal_id, generated_at, valid_until)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (kind, subject, generated_at) do update set detail = excluded.detail, proposal_id = excluded.proposal_id,
			valid_until = excluded.valid_until`,
		id, f.Kind, f.Subject, []byte(f.Detail), f.ProposalID, f.GeneratedAt, f.ValidUntil)
	return store.MapError(err)
}

// Latest returns the newest forecast per (kind, subject), ordered by kind and subject. An empty kind
// matches every kind.
func Latest(ctx context.Context, q store.PolicyQuerier, kind string) ([]Forecast, error) {
	rows, err := q.Query(ctx, `select distinct on (kind, subject) id, kind, subject, detail, proposal_id, generated_at, valid_until
		from ai_forecasts where ($1 = '' or kind = $1) order by kind, subject, generated_at desc`, kind)
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Forecast, error) {
		var f Forecast
		err := row.Scan(&f.ID, &f.Kind, &f.Subject, &f.Detail, &f.ProposalID, &f.GeneratedAt, &f.ValidUntil)
		return f, err
	})
	return out, store.MapError(err)
}
