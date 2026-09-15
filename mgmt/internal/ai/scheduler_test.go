package ai_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

type countAgent struct {
	name    string // "" is capacity_forecast
	runs    atomic.Int32
	active  atomic.Int32
	overlap atomic.Bool
}

func (a *countAgent) Name() string {
	if a.name != "" {
		return a.name
	}
	return "capacity_forecast"
}

func (a *countAgent) Run(ctx context.Context, _ *ai.Run) error {
	if a.active.Add(1) > 1 {
		a.overlap.Store(true)
	}
	defer a.active.Add(-1)
	a.runs.Add(1)
	time.Sleep(300 * time.Millisecond)
	return nil
}

func TestSchedulerRunsOnceAcrossInstances(t *testing.T) {
	st := storetest.New(t)
	cfg := config.AIConfig{AgentStartDelay: 0, Agents: map[string]config.AgentConfig{"capacity_forecast": {Enabled: true, Interval: 2 * time.Second}}}
	a := &countAgent{}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	done := make(chan struct{}, 2)
	for _, id := range []string{"i1", "i2"} {
		s := &ai.Scheduler{Store: st, InstanceID: id, Agents: []ai.Agent{a}, Config: cfg, Tick: 100 * time.Millisecond}
		go func() { _ = s.Run(ctx); done <- struct{}{} }()
	}
	<-ctx.Done()
	<-done
	<-done
	if n := a.runs.Load(); n < 3 || n > 4 || a.overlap.Load() {
		t.Fatalf("runs %d overlap %v, want 3..4 without overlap", n, a.overlap.Load())
	}
	var rows int
	_ = st.Pool.QueryRow(context.Background(), "select count(*) from ai_agent_runs where agent='capacity_forecast' and outcome='ok'").Scan(&rows)
	if rows != int(a.runs.Load()) {
		t.Fatalf("ai_agent_runs rows %d, runs %d", rows, a.runs.Load())
	}
	if testutil.ToFloat64(ai.AgentRuns.WithLabelValues("capacity_forecast", "skipped_locked")) == 0 {
		t.Fatal("no skipped_locked counted")
	}
}

func TestSchedulerRespectsLastRunAndRequests(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := st.Pool.Exec(ctx, `insert into ai_agent_runs(agent, instance_id, started_at, finished_at, outcome)
		values ('capacity_forecast', 'old', now(), now(), 'ok')`); err != nil {
		t.Fatal(err)
	}
	cfg := config.AIConfig{Agents: map[string]config.AgentConfig{
		"capacity_forecast": {Enabled: true, Interval: time.Hour},
		"rpz_suggestions":   {Enabled: false, Interval: time.Hour},
	}}
	a := &countAgent{}
	off := &countAgent{name: "rpz_suggestions"}
	s := &ai.Scheduler{Store: st, InstanceID: "i1", Agents: []ai.Agent{a, off}, Config: cfg, Tick: 100 * time.Millisecond}
	go func() { _ = s.Run(ctx) }()

	time.Sleep(time.Second)
	if n := a.runs.Load(); n != 0 {
		t.Fatalf("agent ran %d times within its interval", n)
	}

	// Positive path first: a request runs the enabled agent at once and is consumed.
	if err := ai.RequestRun(ctx, st, "capacity_forecast", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := ai.RequestRun(ctx, st, "rpz_suggestions", "tester"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if n := a.runs.Load(); n != 1 {
		t.Fatalf("requested agent ran %d times, want 1", n)
	}
	var requests int
	if err := st.Pool.QueryRow(ctx, "select count(*) from ai_agent_requests where agent='capacity_forecast'").Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("request rows %d (err %v), want 0", requests, err)
	}
	if n := off.runs.Load(); n != 0 {
		t.Fatalf("disabled agent ran %d times", n)
	}

	states, err := ai.AgentStates(ctx, st, cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != len(ai.AgentNames) {
		t.Fatalf("%d agent states, want %d", len(states), len(ai.AgentNames))
	}
	for _, s := range states {
		switch s.Name {
		case "capacity_forecast":
			if !s.Enabled || s.LastStarted == nil || s.LastFinished == nil || s.LastOutcome != "ok" || s.Next == nil || s.Interval != time.Hour {
				t.Fatalf("capacity_forecast state %+v", s)
			}
		case "rpz_suggestions":
			if s.Enabled || s.Next != nil || s.LastStarted != nil {
				t.Fatalf("rpz_suggestions state %+v", s)
			}
		}
	}
}
