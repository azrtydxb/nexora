package dnssec

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

const maintenanceBatch = 20

var maintenanceActor = auth.Actor{Type: "system", ID: "dnssec", Name: "system:dnssec"}

// Maintainer re-signs DNSSEC zones whose signatures are due for refresh (zone_dnssec
// next_maintenance_at). Each run holds the session advisory lock dnssec:<zone id>, so one
// management plane instance at a time signs a zone. Task 14 adds key rollover transitions.
type Maintainer struct {
	Store *store.Store
	Zones *zone.Service
	Tick  time.Duration
}

// Run maintains due zones every Tick until ctx ends.
func (m *Maintainer) Run(ctx context.Context) error {
	t := time.NewTicker(m.Tick)
	defer t.Stop()
	for {
		if err := m.runDue(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("list DNSSEC zones due for maintenance", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (m *Maintainer) runDue(ctx context.Context) error {
	rows, err := m.Store.Pool.Query(ctx, `SELECT zone_id FROM zone_dnssec WHERE enabled AND next_maintenance_at <= now()
		ORDER BY next_maintenance_at LIMIT $1`, maintenanceBatch)
	if err != nil {
		return store.MapError(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return store.MapError(err)
	}
	for _, id := range ids {
		if err := m.maintainLocked(ctx, id); err != nil && ctx.Err() == nil {
			slog.Warn("DNSSEC zone maintenance", "zone", id, "err", err)
		}
	}
	return nil
}

// maintainLocked re-signs zone id under its advisory lock when it is still due.
func (m *Maintainer) maintainLocked(ctx context.Context, id uuid.UUID) error {
	conn, err := m.Store.Pool.Acquire(ctx)
	if err != nil {
		return store.MapError(err)
	}
	defer conn.Release()
	key := "dnssec:" + id.String()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", key).Scan(&locked); err != nil {
		return store.MapError(err)
	}
	if !locked {
		return nil
	}
	// If the connection broke, the session (and with it the lock) is already gone.
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(hashtext($1))", key) }()
	var due bool
	err = conn.QueryRow(ctx, "SELECT enabled AND next_maintenance_at <= now() FROM zone_dnssec WHERE zone_id = $1", id).Scan(&due)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !due) {
		return nil // deleted, disabled, or signed by another instance meanwhile
	}
	if err != nil {
		return store.MapError(err)
	}
	_, err = m.Zones.Mutate(ctx, id, func(pgx.Tx, *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		return "dnssecMaintenance", nil, nil, zone.RebuildOptions{}, nil
	}, maintenanceActor)
	return err
}
