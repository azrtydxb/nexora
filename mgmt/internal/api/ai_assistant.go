package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/assistant"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// The configuration assistant is suggest-only: a turn stores at most a proposal, which only
// applyAiProposals turns into configuration. Sessions are private to their owner; others get 404.

// maxAssistantMessage bounds a user message in characters.
const maxAssistantMessage = 2000

func (h *handlers) CreateAiAssistantSession(ctx context.Context, _ CreateAiAssistantSessionRequestObject) (CreateAiAssistantSessionResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	s, err := assistant.CreateSession(ctx, h.d.Store, PrincipalFrom(ctx))
	if err != nil {
		return nil, err
	}
	return CreateAiAssistantSession201JSONResponse(AiAssistantSession{Id: s.ID, Title: s.Title, CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt, Messages: []AiAssistantMessage{}}), nil
}

func (h *handlers) GetAiAssistantSession(ctx context.Context, req GetAiAssistantSessionRequestObject) (GetAiAssistantSessionResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	s, msgs, err := assistant.GetSession(ctx, h.d.Store, req.Id, PrincipalFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := AiAssistantSession{Id: s.ID, Title: s.Title, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
		Messages: make([]AiAssistantMessage, len(msgs))}
	for i, m := range msgs {
		out.Messages[i] = AiAssistantMessage{Id: m.ID, Role: AiAssistantMessageRole(m.Role), Content: m.Content,
			ProposalId: m.ProposalID, CreatedAt: m.CreatedAt}
	}
	pid, err := assistant.CurrentProposalID(ctx, h.d.Store, s.ID)
	if err != nil {
		return nil, err
	}
	if pid != nil {
		p, err := proposal.Get(ctx, h.d.Store.Pool, *pid)
		if err != nil {
			return nil, err
		}
		po := proposalOut(p)
		out.Proposal = &po
	}
	tid, err := assistant.LatestTaskID(ctx, h.d.Store, s.ID)
	if err != nil {
		return nil, err
	}
	if tid != nil {
		t, err := ai.GetTask(ctx, h.d.Store, *tid)
		if err != nil && !errors.Is(err, store.ErrNotFound) { // a task past its 24 h retention is simply gone
			return nil, err
		}
		if err == nil {
			to := taskOut(t)
			out.Task = &to
		}
	}
	return GetAiAssistantSession200JSONResponse(out), nil
}

func (h *handlers) PostAiAssistantMessage(ctx context.Context, req PostAiAssistantMessageRequestObject) (PostAiAssistantMessageResponseObject, error) {
	rt, err := h.aiRuntime()
	if err != nil {
		return nil, err
	}
	if _, _, err := assistant.GetSession(ctx, h.d.Store, req.Id, PrincipalFrom(ctx)); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, invalid("body is required")
	}
	content := req.Body.Content
	if n := utf8.RuneCountInString(content); strings.TrimSpace(content) == "" || n > maxAssistantMessage {
		return nil, invalid("content must be 1 to %d characters", maxAssistantMessage)
	}
	if !rt.TaskKinds[ai.TaskAssistantMessage] {
		return nil, apiError{status: http.StatusServiceUnavailable, code: "feature_disabled", msg: "the AI feature assistant_message is disabled"}
	}
	msg, err := assistant.AddUserMessage(ctx, h.d.Store, req.Id, content)
	if err != nil {
		return nil, err
	}
	t, err := h.startTask(ctx, ai.TaskAssistantMessage, assistant.TaskInput{SessionID: req.Id, MessageID: msg.ID})
	if err != nil {
		return nil, err
	}
	if err := assistant.LinkTask(ctx, h.d.Store, msg.ID, t.Id); err != nil {
		return nil, err
	}
	return PostAiAssistantMessage202JSONResponse(t), nil
}
