package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func waitTask(t *testing.T, st *store.Store, id uuid.UUID) ai.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := ai.GetTask(context.Background(), st, id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == "succeeded" || task.Status == "failed" || time.Now().After(deadline) {
			return task
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTasksLifecycle(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	tasks := ai.NewTasks(ctx, st, "i1")
	release := make(chan struct{})
	tasks.Register(ai.TaskQueryLogSearch, func(ctx context.Context, task ai.Task) (any, error) {
		<-release
		var in map[string]string
		if err := json.Unmarshal(task.Input, &in); err != nil || in["query"] != "q" {
			return nil, errors.New("bad input")
		}
		return map[string]int{"n": 1}, nil
	})
	tasks.Register(ai.TaskThreatCheck, func(ctx context.Context, _ ai.Task) (any, error) {
		return nil, ai.ErrInvalidOutput
	})
	tasks.Register(ai.TaskAssistantMessage, func(ctx context.Context, _ ai.Task) (any, error) {
		return nil, &ai.TaskError{Code: "querylog_unavailable", Message: "down"}
	})

	user := auth.Principal{UserID: "u1", Kind: "session", Role: auth.RoleOperator}
	task, err := tasks.Start(ai.TaskQueryLogSearch, user, map[string]string{"query": "q"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "queued" || task.RequestedBy != "u1" || task.RequesterKind != "session" {
		t.Fatalf("started task %+v", task)
	}
	got, err := ai.GetTask(ctx, st, task.ID)
	if err != nil || (got.Status != "queued" && got.Status != "running") || got.StartedAt != nil && got.Status == "queued" {
		t.Fatalf("before release %+v (err %v)", got, err)
	}
	close(release)
	got = waitTask(t, st, task.ID)
	if got.Status != "succeeded" || string(got.Result) != `{"n": 1}` || got.StartedAt == nil || got.FinishedAt == nil || got.ErrorCode != "" {
		t.Fatalf("succeeded task %+v result %s", got, got.Result)
	}

	token := auth.Principal{UserID: "u1", TokenID: "t1", Kind: "api_token", Role: auth.RoleOperator}
	task, err = tasks.Start(ai.TaskThreatCheck, token, map[string]any{"domains": []string{"a.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if got = waitTask(t, st, task.ID); got.Status != "failed" || got.ErrorCode != "invalid_output" || got.RequestedBy != "t1" || got.RequesterKind != "api_token" {
		t.Fatalf("invalid output task %+v", got)
	}

	task, err = tasks.Start(ai.TaskAssistantMessage, user, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if got = waitTask(t, st, task.ID); got.Status != "failed" || got.ErrorCode != "querylog_unavailable" || got.ErrorMessage != "down" || got.Result != nil {
		t.Fatalf("task error task %+v", got)
	}

	if _, err := ai.GetTask(ctx, st, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing task err %v, want ErrNotFound", err)
	}
	if _, err := ai.NewTasks(ctx, st, "i1").Start(ai.TaskQueryLogSearch, user, nil); err == nil {
		t.Fatal("unregistered kind started")
	}
}

func TestTasksInstanceLoss(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `insert into instances(id, heartbeat_at) values ('dead', now() - interval '60 seconds'), ('alive', now())`); err != nil {
		t.Fatal(err)
	}
	ids := map[string]uuid.UUID{}
	for _, inst := range []string{"dead", "alive"} {
		var id uuid.UUID
		if err := st.Pool.QueryRow(ctx, `insert into ai_tasks(kind, status, requested_by, requester_kind, instance_id, input, started_at)
			values ('querylog_search', 'running', 'u1', 'session', $1, '{}', now()) returning id`, inst).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[inst] = id
	}
	if err := ai.Prune(ctx, st, time.Now()); err != nil {
		t.Fatal(err)
	}
	dead, err := ai.GetTask(ctx, st, ids["dead"])
	if err != nil || dead.Status != "failed" || dead.ErrorCode != "instance_stopped" || dead.FinishedAt == nil {
		t.Fatalf("task of a stopped instance %+v (err %v)", dead, err)
	}
	alive, err := ai.GetTask(ctx, st, ids["alive"])
	if err != nil || alive.Status != "running" {
		t.Fatalf("task of a live instance %+v (err %v)", alive, err)
	}
}
