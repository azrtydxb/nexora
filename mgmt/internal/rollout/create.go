package rollout

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateParams describes the rollout of one group snapshot.
type CreateParams struct {
	EngineGroupID uuid.UUID
	Version       uint64
	FromVersion   *uint64
	Kind          Kind
	// Immediate rolls out to every engine at once, ignoring the group's strategy and pause: the
	// content equals the group's stable content, or a test published the snapshot raw.
	Immediate bool
	Actor     string
}

// Create supersedes the engine group's open rollouts, inserts the new rollout (rolling for
// all_at_once, pending otherwise) and notifies nexora_rollout inside tx.
func Create(ctx context.Context, tx pgx.Tx, p CreateParams) (uuid.UUID, State, error) {
	var paused bool
	var groupStrategy string
	if err := tx.QueryRow(ctx, `select rollouts_paused, rollout_strategy from engine_groups where id = $1 for no key update`,
		p.EngineGroupID).Scan(&paused, &groupStrategy); err != nil {
		return uuid.Nil, "", fmt.Errorf("rollout: engine group %s: %w", p.EngineGroupID, err)
	}
	held := p.Kind == KindChange && !p.Immediate && paused
	supersede := []string{"pending", "canary", "verifying", "rolling", "halted"}
	switch {
	case held:
		supersede = []string{"pending"}
	case p.Kind == KindRollback:
		if _, err := tx.Exec(ctx, `update rollouts set state = 'rolled_back', finished_at = now(), updated_at = now()
			where engine_group_id = $1 and state = 'halted'`, p.EngineGroupID); err != nil {
			return uuid.Nil, "", err
		}
		supersede = []string{"pending", "canary", "verifying", "rolling"}
		if _, err := tx.Exec(ctx, `update engine_groups set rollouts_paused = true, updated_at = now() where id = $1`, p.EngineGroupID); err != nil {
			return uuid.Nil, "", err
		}
	}
	if _, err := tx.Exec(ctx, `update rollouts set state = 'superseded', finished_at = now(), updated_at = now()
		where engine_group_id = $1 and state = any($2)`, p.EngineGroupID, supersede); err != nil {
		return uuid.Nil, "", err
	}
	strategy := Strategy(groupStrategy)
	if p.Kind != KindChange || p.Immediate {
		strategy = AllAtOnce
	}
	state := Pending
	if strategy == AllAtOnce && !held {
		state = Rolling
	}
	var from *int64
	if p.FromVersion != nil {
		f := int64(*p.FromVersion)
		from = &f
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		insert into rollouts (engine_group_id, version, from_version, kind, strategy, state, params, created_by, phase_started_at)
		select g.id, $2, $3, $4, $5, $6,
		       jsonb_build_object('strategy', $5::text, 'canary_count', g.canary_count, 'canary_percent', g.canary_percent,
		         'ack_timeout_seconds', g.ack_timeout_seconds, 'health_window_seconds', g.health_window_seconds,
		         'max_servfail_ratio', g.max_servfail_ratio, 'min_health_queries', g.min_health_queries),
		       $7, case when $6 = 'rolling' then now() end
		from engine_groups g where g.id = $1
		returning id`, p.EngineGroupID, int64(p.Version), from, string(p.Kind), string(strategy), string(state), p.Actor).Scan(&id)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("rollout: insert: %w", err)
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nexora_rollout', $1::text)`, p.EngineGroupID); err != nil {
		return uuid.Nil, "", err
	}
	return id, state, nil
}
