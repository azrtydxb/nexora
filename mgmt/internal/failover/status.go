package failover

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Observation is reported by a fenced frontend adapter, never by CRUD callers.
// It is not proof of ownership and must not be used to grant a lease.
type Observation struct {
	AppliedGeneration    int64
	Owner, State, Reason string
	ObservedAt           time.Time
}

// GetObservation returns nil when no adapter has reported. No write API is
// exposed until the adapter's ownership/fencing protocol exists.
func GetObservation(ctx context.Context, q store.PolicyQuerier, id uuid.UUID) (*Observation, error) {
	var o Observation
	err := q.QueryRow(ctx, `select applied_generation, owner, state, reason, observed_at
		from public.failover_observations where group_id=$1`, id).Scan(&o.AppliedGeneration, &o.Owner, &o.State, &o.Reason, &o.ObservedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, store.MapError(err)
	}
	return &o, nil
}

// Status projects an observation conservatively. Missing/stale/future reports or
// reports for another generation cannot establish current frontend health.
func (g Group) Status(o *Observation, now time.Time) string {
	if g.Lifecycle != "" && g.Lifecycle != "active" {
		return "pending"
	}
	if o == nil || o.Owner == "" || o.ObservedAt.IsZero() || o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > 30*time.Second {
		return "unknown"
	}
	if g.Generation <= 0 || o.AppliedGeneration != g.Generation {
		return "pending"
	}
	switch o.State {
	case "healthy", "degraded", "unavailable":
		return o.State
	default:
		return "unknown"
	}
}
