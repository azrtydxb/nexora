package ai_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// slowModel holds every call for 200 ms and records the most calls in flight at once.
type slowModel struct {
	*aitest.MockModel
	now, max, gaugeMax atomic.Int64
}

func (m *slowModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	n := m.now.Add(1)
	defer m.now.Add(-1)
	for cur := m.max.Load(); n > cur && !m.max.CompareAndSwap(cur, n); cur = m.max.Load() {
	}
	if g := int64(testutil.ToFloat64(ai.InflightRequests)); g > m.gaugeMax.Load() {
		m.gaugeMax.Store(g)
	}
	time.Sleep(200 * time.Millisecond)
	return m.MockModel.Generate(ctx, call)
}

func TestServiceBounds(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	req := func(p ai.Priority) ai.Request[answer] {
		return ai.Request[answer]{Feature: "bounds", Priority: p, System: "s", Prompt: "p"}
	}
	responses := func(n int) []*provider.Response {
		var rs []*provider.Response
		for range n {
			rs = append(rs, aifake.JSON(answer{"x", 0.5}))
		}
		return rs
	}

	// Concurrency 2: six parallel calls never have more than two in flight.
	slow := &slowModel{MockModel: aifake.Model(responses(6)...)}
	svc := aifake.Service(t, st, slow, func(c *config.AIConfig) { c.MaxConcurrency = 2 })
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			_, err := ai.Generate(ctx, svc, req(ai.Background))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if slow.max.Load() != 2 || slow.gaugeMax.Load() != 2 || testutil.ToFloat64(ai.InflightRequests) != 0 {
		t.Fatalf("in flight max %d, gauge max %d, want 2", slow.max.Load(), slow.gaugeMax.Load())
	}

	// 3 requests per minute: three interactive calls start, the fourth gets ErrBusy after the slot wait.
	frozen := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	c := aifake.Config(t)
	c.RequestsPerMinute = 3
	m := aifake.Model(responses(3)...)
	svc, _, err := ai.New(ctx, ai.Options{Config: c, Store: st, Model: m, Now: func() time.Time { return frozen }, SlotWait: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := ai.Generate(ctx, svc, req(ai.Interactive)); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	limited := testutil.ToFloat64(ai.Requests.WithLabelValues("bounds", "rate_limited"))
	start := time.Now()
	if _, err := ai.Generate(ctx, svc, req(ai.Interactive)); !errors.Is(err, ai.ErrBusy) || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("4th call: %v after %v, want ErrBusy after the slot wait", err, time.Since(start))
	}
	if len(m.RecordedCalls()) != 3 || testutil.ToFloat64(ai.Requests.WithLabelValues("bounds", "rate_limited")) != limited+1 {
		t.Fatalf("rate limited call reached the model or was not counted")
	}

	// Budget 1000 with 850 used: background stops at 80%, interactive still runs.
	if _, err := st.Pool.Exec(ctx, `delete from ai_usage`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into ai_usage (day, feature, input_tokens) values ((now() at time zone 'utc')::date, 'seed', 850)`); err != nil {
		t.Fatal(err)
	}
	budget := func(c *config.AIConfig) { c.DailyTokenBudget = 1000 }
	bm := aifake.Model(responses(1)...)
	one, two := aifake.Service(t, st, bm, budget), aifake.Service(t, st, bm, budget)
	if b, err := two.Budget(ctx); err != nil || b.UsedTokens != 850 || b.LimitTokens != 1000 || b.BackgroundLimitTokens != 800 {
		t.Fatalf("second instance budget %+v %v", b, err)
	}
	if _, err := ai.Generate(ctx, one, req(ai.Background)); !errors.Is(err, ai.ErrBudgetExhausted) || len(bm.RecordedCalls()) != 0 {
		t.Fatalf("background at 85%%: %v", err)
	}
	if _, err := ai.Generate(ctx, one, req(ai.Interactive)); err != nil {
		t.Fatalf("interactive at 85%%: %v", err)
	}
	// 850 + 150 = 1000 used, counted across both instances.
	if b, err := two.Budget(ctx); err != nil || b.UsedTokens != 1000 {
		t.Fatalf("second instance after a call on the first: %+v %v", b, err)
	}
	if _, err := ai.Generate(ctx, two, req(ai.Interactive)); !errors.Is(err, ai.ErrBudgetExhausted) || ai.Code(err) != "budget_exhausted" {
		t.Fatalf("interactive at 100%%: %v", err)
	}
}
