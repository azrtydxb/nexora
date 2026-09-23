package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

type budgetAnswer struct {
	Value string `json:"value"`
}

func budgetResponse(text string, tokens int) *provider.Response {
	return &provider.Response{Content: []provider.ContentPart{provider.TextPart{Text: text}}, FinishReason: provider.FinishStop, Usage: provider.Usage{InputTokens: tokens, TotalTokens: tokens}}
}
func budgetService(st *store.Store, m provider.LanguageModel) *Service {
	now := time.Now()
	return &Service{st: st, model: m, cfg: config.AIConfig{BaseURL: "http://127.0.0.1/v1", DailyTokenBudget: 150, BackgroundBudgetPercent: 80, ValidationAttempts: 3, Timeout: time.Second, MaxTokens: 100}, now: func() time.Time { return now }, slotWait: time.Second, sem: make(chan struct{}, 1), bucket: newTokenBucket(20, now)}
}

type heldBudgetModel struct {
	*aitest.MockModel
	started, release chan struct{}
}

func (m *heldBudgetModel) Generate(ctx context.Context, c provider.Call) (*provider.Response, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return m.MockModel.Generate(ctx, c)
}

func TestBudgetCutsOffValidationAndLengthRetry(t *testing.T) {
	for _, length := range []bool{false, true} {
		t.Run(map[bool]string{false: "validation", true: "length"}[length], func(t *testing.T) {
			st := storetest.New(t)
			first := budgetResponse("invalid", 150)
			if length {
				first.FinishReason = provider.FinishLength
			}
			m := &aitest.MockModel{Responses: []*provider.Response{first, budgetResponse(`{"value":"unexpected"}`, 0)}, Caps: provider.Capabilities{NativeJSON: true}}
			svc := budgetService(st, m)
			res, err := Generate(context.Background(), svc, Request[budgetAnswer]{Feature: "budget-retry"})
			if !errors.Is(err, ErrBudgetExhausted) || len(m.RecordedCalls()) != 1 || res.Usage.TotalTokens != 150 {
				t.Fatalf("result=%+v err=%v calls=%d", res, err, len(m.RecordedCalls()))
			}
		})
	}
}

func TestBudgetQueuedDispatch(t *testing.T) {
	for _, cross := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-instance", true: "cross-instance"}[cross], func(t *testing.T) {
			st := storetest.New(t)
			tokens := 150
			if cross {
				tokens = 0
			}
			m := &heldBudgetModel{MockModel: &aitest.MockModel{Responses: []*provider.Response{budgetResponse(`{"value":"first"}`, tokens), budgetResponse(`{"value":"unexpected"}`, 0)}, Caps: provider.Capabilities{NativeJSON: true}}, started: make(chan struct{}, 2), release: make(chan struct{})}
			svc := budgetService(st, m)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			first := make(chan error, 1)
			go func() { _, err := Generate(ctx, svc, Request[budgetAnswer]{Feature: "first"}); first <- err }()
			select {
			case <-m.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			second := make(chan error, 1)
			go func() { _, err := Generate(ctx, svc, Request[budgetAnswer]{Feature: "queued"}); second <- err }()
			// The second rate token is consumed only after B's initial budget read. A still holds the slot.
			for {
				svc.bucket.mu.Lock()
				queued := svc.bucket.tokens == 18
				svc.bucket.mu.Unlock()
				if queued {
					break
				}
				select {
				case <-ctx.Done():
					close(m.release)
					t.Fatal("B never queued")
				case <-time.After(time.Millisecond):
				}
			}
			if cross {
				otherModel := &aitest.MockModel{Responses: []*provider.Response{budgetResponse(`{"value":"other-instance"}`, 150)}, Caps: provider.Capabilities{NativeJSON: true}}
				other := budgetService(st, otherModel)
				if _, err := Generate(ctx, other, Request[budgetAnswer]{Feature: "other"}); err != nil {
					close(m.release)
					t.Fatal(err)
				}
			}
			close(m.release)
			if err := <-first; err != nil {
				t.Fatal(err)
			}
			if err := <-second; !errors.Is(err, ErrBudgetExhausted) {
				t.Fatalf("queued: %v", err)
			}
			if len(m.RecordedCalls()) != 1 {
				t.Fatalf("calls=%d", len(m.RecordedCalls()))
			}
		})
	}
}

type cancelledBudgetModel struct {
	*aitest.MockModel
	cancel context.CancelFunc
}

func (m *cancelledBudgetModel) Generate(context.Context, provider.Call) (*provider.Response, error) {
	m.cancel()
	return budgetResponse("", 150), context.Canceled
}
func TestBudgetRecordsReturnedUsageAfterCancellation(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &cancelledBudgetModel{MockModel: &aitest.MockModel{Caps: provider.Capabilities{NativeJSON: true}}, cancel: cancel}
	svc := budgetService(st, m)
	_, err := Generate(ctx, svc, Request[budgetAnswer]{Feature: "cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	budget, err := svc.Budget(context.Background())
	if err != nil || budget.UsedTokens != 150 {
		t.Fatalf("budget=%+v err=%v", budget, err)
	}
}
