package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// AgentNames lists the background agents in display order (fixed by the M11 spec).
var AgentNames = []string{"querylog_anomalies", "dashboard_insights", "filter_recommendations", "upstream_prediction",
	"rollout_risk", "threat_classification", "capacity_forecast", "rpz_suggestions"}

// Agent metrics (names fixed by the M11 spec).
var (
	AgentRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nexora_mgmt_ai_agent_runs_total", Help: "AI agent runs by agent and outcome",
	}, []string{"agent", "outcome"})
	AgentLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nexora_mgmt_ai_agent_last_success_timestamp_seconds", Help: "Unix time of each AI agent's last ok or no_change run",
	}, []string{"agent"})
)

const (
	// DefaultAgentTick is how often a scheduler looks for due agents.
	DefaultAgentTick = 5 * time.Second
	// minAgentTimeout is the shortest run deadline; a run's deadline is max(interval, minAgentTimeout).
	minAgentTimeout  = 10 * time.Minute
	maxRunErrorBytes = 500
	finishTimeout    = 10 * time.Second
)

// Run is one agent run. The agent sets Outcome ("ok" by default, "no_change" or "skipped_budget"),
// Detail and the summed Usage of its model calls.
type Run struct {
	ID      int64
	Agent   string
	Started time.Time
	Outcome string
	Detail  map[string]any
	Usage   provider.Usage
}

// Agent is a background AI agent.
type Agent interface {
	Name() string // one of AgentNames
	Run(ctx context.Context, run *Run) error
}

// Scheduler runs due agents. A run holds the session advisory lock nexora:ai:agent:<name>, so one
// instance at a time runs an agent, and runs of an agent never overlap.
type Scheduler struct {
	Store      *store.Store
	InstanceID string
	Agents     []Agent
	Config     config.AIConfig
	Tick       time.Duration // 0: DefaultAgentTick
	Now        func() time.Time

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

// Run schedules agents every Tick until ctx ends, then waits for the runs in progress.
func (s *Scheduler) Run(ctx context.Context) error {
	if s.Tick <= 0 {
		s.Tick = DefaultAgentTick
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	s.running = map[string]bool{}
	start := s.Now()
	defer s.wg.Wait()
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	for {
		for _, a := range s.Agents {
			s.consider(ctx, a, start)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// consider starts a goroutine for a due, enabled agent that this instance is not already running.
func (s *Scheduler) consider(ctx context.Context, a Agent, start time.Time) {
	name := a.Name()
	cfg := s.Config.Agents[name]
	if !cfg.Enabled || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	busy := s.running[name]
	s.mu.Unlock()
	if busy {
		return
	}
	due, err := s.due(ctx, s.Store.Pool, name, cfg.Interval, start)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("AI agent schedule check failed", "agent", name, "err", err)
		}
		return
	}
	if !due {
		return
	}
	s.mu.Lock()
	s.running[name] = true
	s.mu.Unlock()
	s.wg.Go(func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, name)
			s.mu.Unlock()
		}()
		if err := s.attempt(ctx, a, cfg, start); err != nil && ctx.Err() == nil {
			slog.Warn("AI agent run bookkeeping failed", "agent", name, "err", err)
		}
	})
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// due: a Run now request exists, the newest run started at least interval ago, or the agent never ran
// and the instance started at least AgentStartDelay ago.
func (s *Scheduler) due(ctx context.Context, q querier, name string, interval time.Duration, start time.Time) (bool, error) {
	var last *time.Time
	var requested bool
	err := q.QueryRow(ctx, `select (select max(started_at) from ai_agent_runs where agent = $1),
		exists (select 1 from ai_agent_requests where agent = $1)`, name).Scan(&last, &requested)
	if err != nil {
		return false, store.MapError(err)
	}
	now := s.Now()
	switch {
	case requested:
		return true, nil
	case last != nil:
		return now.Sub(*last) >= interval, nil
	default:
		return now.Sub(start) >= s.Config.AgentStartDelay, nil
	}
}

// attempt takes the agent lock on a dedicated connection and, when still due, runs the agent once.
func (s *Scheduler) attempt(ctx context.Context, a Agent, cfg config.AgentConfig, start time.Time) error {
	name := a.Name()
	conn, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return store.MapError(err)
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock(hashtext('nexora:ai:agent:' || $1))`, name).Scan(&locked); err != nil {
		return store.MapError(err)
	}
	if !locked {
		AgentRuns.WithLabelValues(name, "skipped_locked").Inc()
		return nil
	}
	defer unlock(ctx, conn, `select pg_advisory_unlock(hashtext('nexora:ai:agent:' || $1))`, name)

	run, err := s.begin(ctx, conn, name, cfg.Interval, start)
	if err != nil || run == nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, max(cfg.Interval, minAgentTimeout))
	began := time.Now()
	runErr := a.Run(runCtx, run)
	cancel()
	duration := time.Since(began)

	outcome, errText := run.Outcome, ""
	switch {
	case runErr != nil:
		outcome, errText = "failed", truncate(runErr.Error(), maxRunErrorBytes)
	case outcome != "ok" && outcome != "no_change" && outcome != "skipped_budget":
		outcome, errText = "failed", truncate(fmt.Sprintf("agent set unknown outcome %q", outcome), maxRunErrorBytes)
	}
	detail := []byte("{}")
	if len(run.Detail) > 0 {
		if b, err := json.Marshal(run.Detail); err == nil {
			detail = b
		}
	}
	AgentRuns.WithLabelValues(name, outcome).Inc()
	finished := s.Now()
	if outcome == "ok" || outcome == "no_change" {
		AgentLastSuccess.WithLabelValues(name).Set(float64(finished.Unix()))
	}
	if outcome == "failed" {
		slog.Warn("AI agent run failed", "agent", name, "outcome", outcome, "duration", duration,
			"input_tokens", run.Usage.InputTokens, "output_tokens", run.Usage.OutputTokens, "err", errText)
	}
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer fcancel()
	_, err = conn.Exec(fctx, `update ai_agent_runs set finished_at = $2, outcome = $3, input_tokens = $4,
		output_tokens = $5, detail = $6, error = $7 where id = $1`,
		run.ID, finished, outcome, run.Usage.InputTokens, run.Usage.OutputTokens, detail, errText)
	return store.MapError(err)
}

// begin re-checks due-ness inside the lock, consumes the Run now request and records the run start.
// It returns nil when the agent is no longer due.
func (s *Scheduler) begin(ctx context.Context, conn *pgxpool.Conn, name string, interval time.Duration, start time.Time) (*Run, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, store.MapError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	due, err := s.due(ctx, tx, name, interval, start)
	if err != nil || !due {
		return nil, err
	}
	run := &Run{Agent: name, Started: s.Now(), Outcome: "ok", Detail: map[string]any{}}
	if _, err := tx.Exec(ctx, `delete from ai_agent_requests where agent = $1`, name); err != nil {
		return nil, store.MapError(err)
	}
	if err := tx.QueryRow(ctx, `insert into ai_agent_runs(agent, instance_id, started_at) values ($1, $2, $3) returning id`,
		name, s.InstanceID, run.Started).Scan(&run.ID); err != nil {
		return nil, store.MapError(err)
	}
	return run, store.MapError(tx.Commit(ctx))
}

// unlock releases a session advisory lock. A connection whose unlock fails is closed, so the pool
// never hands out a connection that still holds the lock.
func unlock(ctx context.Context, conn *pgxpool.Conn, sql string, args ...any) {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if _, err := conn.Exec(uctx, sql, args...); err != nil {
		_ = conn.Conn().Close(uctx)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// RequestRun asks for an immediate run of agent; the next scheduler tick on any instance runs it.
func RequestRun(ctx context.Context, st *store.Store, agent, by string) error {
	_, err := st.Pool.Exec(ctx, `insert into ai_agent_requests(agent, requested_by) values ($1, $2)
		on conflict (agent) do update set requested_at = now(), requested_by = excluded.requested_by`, agent, by)
	return store.MapError(err)
}

// AgentState is one agent's schedule and last run for getAiStatus.
type AgentState struct {
	Name                            string
	Enabled, Running                bool
	Interval                        time.Duration
	LastStarted, LastFinished, Next *time.Time
	LastOutcome, LastError          string
}

// AgentStates reports every agent in AgentNames order. Running means the newest run has not finished
// and its instance heartbeat is fresh; Next is nil for a disabled agent.
func AgentStates(ctx context.Context, st *store.Store, c config.AIConfig, instanceStart time.Time) ([]AgentState, error) {
	states := make([]AgentState, len(AgentNames))
	index := make(map[string]int, len(AgentNames))
	for i, name := range AgentNames {
		cfg := c.Agents[name]
		states[i] = AgentState{Name: name, Enabled: cfg.Enabled, Interval: cfg.Interval}
		index[name] = i
	}
	rows, err := st.Pool.Query(ctx, `select distinct on (r.agent) r.agent, r.started_at, r.finished_at, r.outcome, r.error,
			r.finished_at is null and exists (select 1 from instances i where i.id = r.instance_id
				and i.heartbeat_at >= now() - interval '15 seconds')
		from ai_agent_runs r where r.agent = any($1) order by r.agent, r.started_at desc`, AgentNames)
	if err != nil {
		return nil, store.MapError(err)
	}
	for rows.Next() {
		var name string
		var started time.Time
		var finished *time.Time
		var outcome, errText string
		var running bool
		if err := rows.Scan(&name, &started, &finished, &outcome, &errText, &running); err != nil {
			rows.Close()
			return nil, store.MapError(err)
		}
		s := &states[index[name]]
		s.LastStarted, s.LastFinished, s.LastOutcome, s.LastError, s.Running = &started, finished, outcome, errText, running
	}
	if err := rows.Err(); err != nil {
		return nil, store.MapError(err)
	}
	requests := map[string]time.Time{}
	rows, err = st.Pool.Query(ctx, `select agent, requested_at from ai_agent_requests`)
	if err != nil {
		return nil, store.MapError(err)
	}
	for rows.Next() {
		var name string
		var at time.Time
		if err := rows.Scan(&name, &at); err != nil {
			rows.Close()
			return nil, store.MapError(err)
		}
		requests[name] = at
	}
	if err := rows.Err(); err != nil {
		return nil, store.MapError(err)
	}
	for i := range states {
		s := &states[i]
		if !s.Enabled {
			continue
		}
		var next time.Time
		switch at, ok := requests[s.Name]; {
		case ok:
			next = at
		case s.LastStarted != nil:
			next = s.LastStarted.Add(s.Interval)
		default:
			next = instanceStart.Add(c.AgentStartDelay)
		}
		s.Next = &next
	}
	return states, nil
}
