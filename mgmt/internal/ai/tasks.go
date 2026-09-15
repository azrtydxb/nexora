package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// TaskKind is the kind of an asynchronous interactive AI task.
type TaskKind string

const (
	TaskQueryLogSearch   TaskKind = "querylog_search"
	TaskThreatCheck      TaskKind = "threat_check"
	TaskAssistantMessage TaskKind = "assistant_message"
)

// Task is one row of ai_tasks.
type Task struct {
	ID                      uuid.UUID
	Kind                    TaskKind
	Status                  string // queued|running|succeeded|failed
	RequestedBy             string // Principal.UserID, or TokenID for an API token
	RequesterKind           string // Principal.Kind
	Input, Result           json.RawMessage
	ErrorCode, ErrorMessage string
	CreatedAt               time.Time
	StartedAt, FinishedAt   *time.Time
}

// TaskFunc performs a task and returns its JSON-encodable result. Errors map to a task error code
// through Code, or carry their own code as a *TaskError.
type TaskFunc func(ctx context.Context, t Task) (any, error)

// TaskError is a task failure with a feature-specific code, e.g. querylog_unavailable.
type TaskError struct{ Code, Message string }

func (e *TaskError) Error() string { return e.Code + ": " + e.Message }

// Tasks runs registered task kinds in goroutines of this instance and records them in ai_tasks.
type Tasks struct {
	ctx        context.Context
	st         *store.Store
	instanceID string
	mu         sync.RWMutex
	funcs      map[TaskKind]TaskFunc
}

// NewTasks returns a runner whose tasks end with ctx, the serve context. A task cut off by shutdown
// is closed as failed with instance_stopped.
func NewTasks(ctx context.Context, st *store.Store, instanceID string) *Tasks {
	return &Tasks{ctx: ctx, st: st, instanceID: instanceID, funcs: map[TaskKind]TaskFunc{}}
}

// Register sets the function of kind.
func (t *Tasks) Register(kind TaskKind, f TaskFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.funcs[kind] = f
}

// Start records a queued task for p and runs it in a goroutine. It fails for an unregistered kind.
func (t *Tasks) Start(kind TaskKind, p auth.Principal, input any) (Task, error) {
	t.mu.RLock()
	f := t.funcs[kind]
	t.mu.RUnlock()
	if f == nil {
		return Task{}, fmt.Errorf("ai: task kind %q is not registered", kind)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Task{}, fmt.Errorf("ai: task input: %w", err)
	}
	task := Task{Kind: kind, Status: "queued", RequestedBy: p.UserID, RequesterKind: p.Kind, Input: raw}
	if p.Kind == "api_token" {
		task.RequestedBy = p.TokenID
	}
	if err := t.st.Pool.QueryRow(t.ctx, `insert into ai_tasks(kind, status, requested_by, requester_kind, instance_id, input)
		values ($1, 'queued', $2, $3, $4, $5) returning id, created_at`,
		kind, task.RequestedBy, task.RequesterKind, t.instanceID, raw).Scan(&task.ID, &task.CreatedAt); err != nil {
		return Task{}, store.MapError(err)
	}
	go t.run(task, f)
	return task, nil
}

func (t *Tasks) run(task Task, f TaskFunc) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), finishTimeout)
	_, err := t.st.Pool.Exec(fctx, `update ai_tasks set status = 'running', started_at = now() where id = $1`, task.ID)
	cancel()
	if err != nil {
		slog.Warn("AI task start failed", "task", task.ID, "kind", task.Kind, "err", err)
		return
	}
	task.Status = "running"
	result, runErr := f(t.ctx, task)

	status, code, message := "succeeded", "", ""
	var raw []byte
	if runErr == nil {
		if raw, err = json.Marshal(result); err != nil {
			runErr = fmt.Errorf("encode result: %w", err)
		}
	}
	if runErr != nil {
		status, raw = "failed", nil
		var te *TaskError
		switch {
		case t.ctx.Err() != nil:
			code, message = "instance_stopped", "the management plane instance stopped"
		case errors.As(runErr, &te):
			code, message = te.Code, te.Message
		case Code(runErr) != "":
			code, message = Code(runErr), runErr.Error()
		default:
			code, message = "internal_error", "internal error"
		}
		slog.Warn("AI task failed", "task", task.ID, "kind", task.Kind, "code", code, "err", runErr)
	}
	fctx, cancel = context.WithTimeout(context.WithoutCancel(t.ctx), finishTimeout)
	defer cancel()
	if _, err := t.st.Pool.Exec(fctx, `update ai_tasks set status = $2, result = $3, error_code = $4, error_message = $5,
		finished_at = now() where id = $1`, task.ID, status, raw, code, message); err != nil {
		slog.Warn("AI task finish failed", "task", task.ID, "kind", task.Kind, "err", err)
	}
}

// GetTask reads one task, or store.ErrNotFound. A queued or running task whose instance stopped is
// closed as failed with instance_stopped first, so a poller never waits for the next prune.
func GetTask(ctx context.Context, st *store.Store, id uuid.UUID) (Task, error) {
	if err := failStoppedTasks(ctx, st, "id = $1", id); err != nil {
		return Task{}, err
	}
	var task Task
	var kind string
	err := st.Pool.QueryRow(ctx, `select id, kind, status, requested_by, requester_kind, input, result, error_code,
		error_message, created_at, started_at, finished_at from ai_tasks where id = $1`, id).
		Scan(&task.ID, &kind, &task.Status, &task.RequestedBy, &task.RequesterKind, &task.Input, &task.Result,
			&task.ErrorCode, &task.ErrorMessage, &task.CreatedAt, &task.StartedAt, &task.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, store.ErrNotFound
	}
	task.Kind = TaskKind(kind)
	return task, store.MapError(err)
}
