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

// orphanSweepEvery is how often the maintainer looks for PKCS#11 keys no row references.
const orphanSweepEvery = 10 * time.Minute

// Maintainer keeps DNSSEC zones current: for zones due (zone_dnssec next_maintenance_at) it applies
// key rollover transitions and re-signs expiring signatures, each zone under the session advisory
// lock dnssec:<zone id> so one management plane instance at a time signs it. Every run also
// destroys queued PKCS#11 keys of committed removals and, every orphanSweepEvery, token keys no
// committed row references.
type Maintainer struct {
	Store   *store.Store
	Service *Service
	Tick    time.Duration
}

// Run maintains due zones every Tick until ctx ends.
func (m *Maintainer) Run(ctx context.Context) error {
	t := time.NewTicker(m.Tick)
	defer t.Stop()
	var lastSweep time.Time
	for {
		if err := m.runDue(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("list DNSSEC zones due for maintenance", "err", err)
		}
		if err := DestroyPending(ctx, m.Store, m.Service.Box); err != nil && ctx.Err() == nil {
			slog.Warn("destroy removed PKCS#11 signing keys", "err", err)
		}
		if time.Since(lastSweep) >= orphanSweepEvery {
			lastSweep = time.Now()
			if n, err := SweepTokenOrphans(ctx, m.Store, m.Service.Box, OrphanGrace); err != nil && ctx.Err() == nil {
				slog.Warn("sweep orphaned PKCS#11 signing keys", "err", err)
			} else if n > 0 {
				slog.Info("destroyed orphaned PKCS#11 signing keys", "count", n)
			}
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

// maintainLocked advances rollovers of zone id and re-signs it under its advisory lock when it is
// still due.
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
	svc := m.Service
	_, err = svc.Zones.Mutate(ctx, id, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		return "dnssecMaintenance", nil, nil, zone.RebuildOptions{}, svc.advance(ctx, tx, z, svc.now())
	}, maintenanceActor)
	return err
}
