package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// stoppedInstance matches a row whose instance_id names an instance with a heartbeat older than 15 s
// (instances heartbeat every 5 s). An instance without a row is not judged.
const stoppedInstance = `exists (select 1 from instances i where i.id = instance_id
	and i.heartbeat_at < now() - interval '15 seconds')`

// retention is one pruned table: the delete runs when every table in needs exists (tables of later
// M11 tasks may not be migrated yet).
type retention struct {
	needs []string
	sql   string // $1 is the cutoff
	keep  time.Duration
}

var retentions = []retention{
	{nil, `delete from ai_tasks where created_at < $1`, 24 * time.Hour},
	{nil, `delete from ai_agent_runs where started_at < $1`, 30 * 24 * time.Hour},
	{nil, `delete from ai_usage where day < $1::date`, 400 * 24 * time.Hour},
	{[]string{"ai_proposals"}, `delete from ai_proposals
		where status in ('applied', 'failed', 'stale', 'dismissed', 'superseded') and updated_at < $1`, 90 * 24 * time.Hour},
	{[]string{"ai_findings"}, `delete from ai_findings where status in ('resolved', 'dismissed') and updated_at < $1`, 30 * 24 * time.Hour},
	{[]string{"ai_forecasts"}, `delete from ai_forecasts where valid_until < $1`, 7 * 24 * time.Hour},
	{[]string{"ai_domain_verdicts"}, `delete from ai_domain_verdicts where expires_at < $1`, 0},
	{[]string{"ai_assistant_sessions", "ai_assistant_messages"}, `with idle as (
			select id from ai_assistant_sessions where updated_at < $1
		), gone as (
			delete from ai_assistant_messages where session_id in (select id from idle)
		)
		delete from ai_assistant_sessions where id in (select id from idle)`, 30 * 24 * time.Hour},
	{[]string{"ai_capacity_samples"}, `delete from ai_capacity_samples where day < $1::date`, 400 * 24 * time.Hour},
}

// Prune closes the tasks and agent runs of stopped instances as failed with instance_stopped and
// deletes AI rows past their retention. It holds the advisory lock nexora:ai:prune and returns nil
// without work when another instance holds it.
func Prune(ctx context.Context, st *store.Store, now time.Time) error {
	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		return store.MapError(err)
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock(hashtext('nexora:ai:prune'))`).Scan(&locked); err != nil {
		return store.MapError(err)
	}
	if !locked {
		return nil
	}
	defer unlock(ctx, conn, `select pg_advisory_unlock(hashtext('nexora:ai:prune'))`)

	if err := failStoppedTasks(ctx, st, "true"); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `update ai_agent_runs set finished_at = now(), outcome = 'failed', error = 'instance_stopped'
		where finished_at is null and `+stoppedInstance); err != nil {
		return fmt.Errorf("close runs of stopped instances: %w", store.MapError(err))
	}
	for _, r := range retentions {
		present := true
		for _, table := range r.needs {
			if err := conn.QueryRow(ctx, `select to_regclass($1) is not null`, table).Scan(&present); err != nil {
				return store.MapError(err)
			}
			if !present {
				break
			}
		}
		if !present {
			continue
		}
		if _, err := conn.Exec(ctx, r.sql, now.Add(-r.keep)); err != nil {
			return fmt.Errorf("prune: %s: %w", r.sql, store.MapError(err))
		}
	}
	return nil
}

// failStoppedTasks closes the queued and running tasks matching where (its args start at $1) whose
// instance stopped.
func failStoppedTasks(ctx context.Context, st *store.Store, where string, args ...any) error {
	_, err := st.Pool.Exec(ctx, `update ai_tasks set status = 'failed', error_code = 'instance_stopped',
		error_message = 'the management plane instance stopped', finished_at = now()
		where status in ('queued', 'running') and `+stoppedInstance+` and `+where, args...)
	return store.MapError(err)
}
