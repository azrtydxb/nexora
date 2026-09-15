package ai_test

import (
	"context"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestPrune(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	for _, q := range []string{
		`insert into instances(id, heartbeat_at) values ('dead', now() - interval '60 seconds'), ('alive', now())`,
		`insert into ai_tasks(kind, status, requested_by, requester_kind, instance_id, input, created_at) values
			('querylog_search', 'succeeded', 'u', 'session', 'alive', '{"age":"old"}', now() - interval '25 hours'),
			('querylog_search', 'succeeded', 'u', 'session', 'alive', '{"age":"new"}', now() - interval '23 hours')`,
		`insert into ai_agent_runs(agent, instance_id, started_at, finished_at, outcome, error) values
			('capacity_forecast', 'alive', now() - interval '31 days', now() - interval '31 days', 'ok', 'old'),
			('capacity_forecast', 'alive', now() - interval '29 days', now() - interval '29 days', 'ok', 'new'),
			('rpz_suggestions', 'dead', now() - interval '1 minute', null, 'running', 'stale'),
			('rpz_suggestions', 'alive', now() - interval '1 minute', null, 'running', 'live')`,
		`insert into ai_usage(day, feature, requests) values
			(current_date - 401, 'capacity_forecast', 1),
			(current_date - 399, 'capacity_forecast', 2)`,
	} {
		if _, err := st.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := ai.Prune(ctx, st, time.Now()); err != nil {
		t.Fatal(err)
	}
	var tasks string
	if err := st.Pool.QueryRow(ctx, `select string_agg(input->>'age', ',') from ai_tasks`).Scan(&tasks); err != nil || tasks != "new" {
		t.Fatalf("tasks left %q (err %v), want new", tasks, err)
	}
	var runs string
	if err := st.Pool.QueryRow(ctx, `select string_agg(error || ':' || outcome || ':' || (finished_at is not null)::text, ',' order by id) from ai_agent_runs`).Scan(&runs); err != nil ||
		runs != "new:ok:true,instance_stopped:failed:true,live:running:false" {
		t.Fatalf("runs left %q (err %v)", runs, err)
	}
	var usage int
	if err := st.Pool.QueryRow(ctx, `select sum(requests) from ai_usage`).Scan(&usage); err != nil || usage != 2 {
		t.Fatalf("usage left %d (err %v), want 2", usage, err)
	}
}
