package ai

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
)

type retryBudgetModel struct {
	*aitest.MockModel
	calls int
}

func (m *retryBudgetModel) Generate(context.Context, provider.Call) (*provider.Response, error) {
	m.calls++
	return budgetResponse("", 150), sdk.NewAPICallError(503, "", "", "retry")
}
func TestBudgetDispatchGuardCoversSDKRetries(t *testing.T) {
	m := &retryBudgetModel{MockModel: &aitest.MockModel{Caps: provider.Capabilities{NativeJSON: true}}}
	used := 0
	c := &capture{LanguageModel: m, before: func(context.Context) error {
		if used >= 150 {
			return ErrBudgetExhausted
		}
		return nil
	}, after: func(_ context.Context, u provider.Usage) error { used += u.InputTokens; return nil }}
	retries := 2
	_, err := sdk.GenerateObject[budgetAnswer](context.Background(), sdk.GenerateObjectOpts{Model: c, Messages: []provider.Message{provider.UserText("test")}, MaxRetries: &retries})
	if !errors.Is(err, ErrBudgetExhausted) || used != 150 || m.calls != 1 {
		t.Fatalf("err=%v used=%d calls=%d", err, used, m.calls)
	}
}
