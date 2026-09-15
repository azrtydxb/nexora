// Package aifake builds AI services over the go-ai-sdk aitest MockModel for unit tests.
package aifake

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

var usage = provider.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, ReasoningTokens: 30}

// JSON answers with v as JSON text.
func JSON(v any) *provider.Response {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return Text(string(b))
}

// Text answers with s.
func Text(s string) *provider.Response {
	return &provider.Response{Content: []provider.ContentPart{provider.TextPart{Text: s}}, FinishReason: provider.FinishStop, Usage: usage}
}

// Truncated is an empty answer cut off by the token limit.
func Truncated() *provider.Response {
	return &provider.Response{Content: []provider.ContentPart{provider.TextPart{Text: ""}}, FinishReason: provider.FinishLength, Usage: usage}
}

// Model replays rs in order and reports native JSON support.
func Model(rs ...*provider.Response) *aitest.MockModel {
	return &aitest.MockModel{Responses: rs, Caps: provider.Capabilities{NativeJSON: true}}
}

// Config is the defaults of a configured AI (model fake-qwen on http://127.0.0.1:1/v1) with a daily
// budget of 1,000,000 tokens.
func Config(t testing.TB) config.AIConfig {
	t.Helper()
	c, err := config.Load(func(k string) string {
		return map[string]string{
			"NEXORA_DATABASE_URL": "postgres://aifake", "NEXORA_CA_CERT_FILE": "/ca.crt", "NEXORA_CA_KEY_FILE": "/ca.key",
			"NEXORA_AI_BASE_URL": "http://127.0.0.1:1/v1", "NEXORA_AI_MODEL": "fake-qwen", "NEXORA_AI_API_KEY": "test-key-not-secret",
			"NEXORA_AI_DAILY_TOKEN_BUDGET": "1000000",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.AI
}

// Service returns a service over m on st; mutate, when set, adjusts the configuration first.
func Service(t testing.TB, st *store.Store, m provider.LanguageModel, mutate func(*config.AIConfig)) *ai.Service {
	t.Helper()
	c := Config(t)
	if mutate != nil {
		mutate(&c)
	}
	svc, reason, err := ai.New(context.Background(), ai.Options{Config: c, Store: st, Model: m})
	if err != nil || svc == nil {
		t.Fatalf("ai.New: %v (reason %q)", err, reason)
	}
	return svc
}
