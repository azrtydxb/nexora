package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Controller steps every open rollout; any number of instances run one.
type Controller struct {
	Store *store.Store
	Tick  time.Duration // NEXORA_ROLLOUT_TICK; default 1 s
}

// Run steps on every tick and on nexora_rollout notifications until ctx ends.
func (c *Controller) Run(ctx context.Context) {
	tick := c.Tick
	if tick <= 0 {
		tick = time.Second
	}
	wake := make(chan struct{}, 1)
	go c.listen(ctx, wake)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if _, err := c.Step(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("rollout step", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
	}
}

// listen holds a dedicated connection outside the pool, like the hub's LISTEN.
func (c *Controller) listen(ctx context.Context, wake chan<- struct{}) {
	for ctx.Err() == nil {
		conn, err := pgx.ConnectConfig(ctx, c.Store.Pool.Config().ConnConfig)
		if err == nil {
			_, err = conn.Exec(ctx, "listen nexora_rollout")
			for err == nil {
				if _, err = conn.WaitForNotification(ctx); err == nil {
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
			_ = conn.Close(context.WithoutCancel(ctx))
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// Step drives every open rollout once and returns how many changed state.
func (c *Controller) Step(ctx context.Context) (int, error) {
	rows, err := c.Store.Pool.Query(ctx, `select id from rollouts where state in ('pending','canary','verifying','rolling') order by created_at`)
	if err != nil {
		return 0, store.MapError(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, store.MapError(err)
	}
	driven := 0
	var errs []error
	for _, id := range ids {
		ok, err := c.driveOne(ctx, id)
		if err != nil {
			errs = append(errs, err)
		}
		if ok {
			driven++
		}
	}
	return driven, errors.Join(errs...)
}

func (c *Controller) driveOne(ctx context.Context, id uuid.UUID) (bool, error) {
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var locked bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtext('nexora:rollout:' || $1::text))`, id).Scan(&locked); err != nil || !locked {
		return false, err
	}
	// The engine group row before the rollout row, in the order rollout.Create locks them, so a
	// publish superseding this rollout and this step never deadlock.
	if _, err := tx.Exec(ctx, `select 1 from engine_groups where id = (select engine_group_id from rollouts where id = $1)
		for no key update`, id); err != nil {
		return false, err
	}
	var (
		r       Rollout
		version int64
		params  []byte
		phase   *time.Time
		paused  bool
		now     time.Time
	)
	err = tx.QueryRow(ctx, `
		select r.id, r.engine_group_id, r.version, r.kind, r.state, r.canary_engine_ids, r.phase_started_at, r.halt_reason, r.params,
		       g.rollouts_paused, now()
		from rollouts r join engine_groups g on g.id = r.engine_group_id
		where r.id = $1 for update of r`, id).
		Scan(&r.ID, &r.EngineGroupID, &version, &r.Kind, &r.State, &r.CanaryEngineIDs, &phase, &r.HaltReason, &params, &paused, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // deleted with its engine group since it was listed
	}
	if err != nil {
		return false, err
	}
	r.Version = uint64(version)
	if r.State.Terminal() || r.State == Halted {
		return false, nil
	}
	if phase != nil {
		r.PhaseStartedAt = *phase
	}
	if err := json.Unmarshal(params, &r.Params); err != nil {
		return false, err
	}
	engines, err := loadEngines(ctx, tx, r.EngineGroupID)
	if err != nil {
		return false, err
	}
	if r.State == Verifying {
		for i := range engines {
			if !slices.Contains(r.CanaryEngineIDs, engines[i].ID) {
				continue
			}
			if engines[i].Health, err = HealthSince(ctx, tx, engines[i].ID, r.PhaseStartedAt); err != nil {
				return false, err
			}
		}
	}
	next, changed := Step(r, Observation{Now: now, GroupPaused: paused, Engines: engines})
	if !changed {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		update rollouts set state = $2, canary_engine_ids = $3, phase_started_at = $4, halt_reason = $5, updated_at = now(),
		       finished_at = case when $2 = 'completed' then now() else finished_at end
		where id = $1`, next.ID, string(next.State), next.CanaryEngineIDs, next.PhaseStartedAt, next.HaltReason); err != nil {
		return false, err
	}
	if next.State == Completed {
		if _, err := tx.Exec(ctx, `update engine_groups set stable_version = greatest(coalesce(stable_version, 0), $2), updated_at = now()
			where id = $1`, next.EngineGroupID, int64(next.Version)); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nexora_rollout', $1::text)`, next.EngineGroupID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func loadEngines(ctx context.Context, tx pgx.Tx, group uuid.UUID) ([]Engine, error) {
	rows, err := tx.Query(ctx, `
		select e.id, e.node_name, e.labels,
		       (e.connected_instance is not null and coalesce(i.heartbeat_at > now() - interval '15 seconds', false)),
		       e.applied_version, coalesce(e.rejected_version, 0), e.rejected_reason
		from engines e left join instances i on i.id = e.connected_instance
		where e.engine_group_id = $1 and e.revoked_at is null and e.deleted_at is null
		order by e.node_name, e.enrolled_at`, group)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Engine, error) {
		var e Engine
		var applied, rejected int64
		err := row.Scan(&e.ID, &e.Name, &e.Labels, &e.Connected, &applied, &rejected, &e.RejectedReason)
		e.AppliedVersion, e.RejectedVersion = uint64(applied), uint64(rejected)
		return e, err
	})
}

// HealthSince: the baseline is the newest engine_stats sample in (since-60s, since]; then every
// later sample. Counters that went down (engine restart) count from zero.
func HealthSince(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, since time.Time) (Health, error) {
	rows, err := q.Query(ctx, `
		(select at, stats from engine_stats where engine_id = $1 and at <= $2 and at > $2 - interval '60 seconds' order by at desc limit 1)
		union all
		(select at, stats from engine_stats where engine_id = $1 and at > $2 order by at)
		order by at`, engineID, since)
	if err != nil {
		return Health{}, err
	}
	defer rows.Close()
	var h Health
	var first, last *controlv1.Stats
	for rows.Next() {
		var at time.Time
		var raw []byte
		if err := rows.Scan(&at, &raw); err != nil {
			return Health{}, err
		}
		s := &controlv1.Stats{}
		if proto.Unmarshal(raw, s) != nil {
			continue
		}
		if first == nil {
			first = s
		}
		last = s
		h.Samples++
	}
	if h.Samples >= 2 {
		q0, f0 := first.QueriesTotal, first.ServfailTotal
		if last.QueriesTotal < q0 || last.ServfailTotal < f0 {
			q0, f0 = 0, 0
		}
		h.Queries, h.Servfail = last.QueriesTotal-q0, last.ServfailTotal-f0
	}
	return h, rows.Err()
}
