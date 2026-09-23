package ai

import (
	"context"
	"strings"

	sdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/go-ai-sdk/providers/openai"

	"github.com/piwi3910/nexora/mgmt/internal/config"
)

// NewModel builds the OpenAI-compatible model for c. It is the only place Nexora constructs a
// provider; <think> spans become reasoning parts and never reach the decoded text.
func NewModel(c config.AIConfig) provider.LanguageModel {
	return newModel(c, nil)
}

func newModel(c config.AIConfig, resolve Resolver) provider.LanguageModel {
	m := openai.New(openai.WithBaseURL(strings.TrimRight(c.BaseURL, "/")), openai.WithAPIKey(c.APIKey), openai.WithHTTPClient(providerClient(c.AllowPublicEndpoint, resolve, nil))).Model(c.Model)
	return sdk.ExtractReasoningMiddleware(m, sdk.ExtractReasoningOpts{TagName: "think"})
}

// promptSchemaModel implements NEXORA_AI_STRUCTURED_OUTPUT=prompt. It claims native JSON so that
// GenerateObject hands it the generated schema, then moves that schema from response_format into the
// system message. go-ai-sdk keeps its schema generator internal, so this is how it is reused.
type promptSchemaModel struct{ provider.LanguageModel }

func (promptSchemaModel) Capabilities() provider.Capabilities {
	return provider.Capabilities{NativeJSON: true}
}

func (m promptSchemaModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	if rf := call.ResponseFormat; rf != nil && len(rf.Schema) > 0 && len(call.Messages) > 0 && call.Messages[0].Role == provider.RoleSystem {
		call.ResponseFormat = nil
		msgs := append([]provider.Message(nil), call.Messages...)
		msgs[0] = provider.SystemText(messageText(msgs[0]) + "\n\nAnswer with only one JSON object matching this JSON schema:\n" + string(rf.Schema))
		call.Messages = msgs
	}
	return m.LanguageModel.Generate(ctx, call)
}

func messageText(msg provider.Message) string {
	var sb strings.Builder
	for _, p := range msg.Content {
		if t, ok := p.(provider.TextPart); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}
