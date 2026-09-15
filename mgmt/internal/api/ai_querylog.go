package api

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/qlsearch"
)

// maxQueryLogQuestion is the longest natural-language query-log question, in characters.
const maxQueryLogQuestion = 500

// StartAiQueryLogSearch starts a natural-language query-log search task for the caller.
func (h *handlers) StartAiQueryLogSearch(ctx context.Context, req StartAiQueryLogSearchRequestObject) (StartAiQueryLogSearchResponseObject, error) {
	rt, err := h.aiRuntime()
	if err != nil {
		return nil, err
	}
	if !rt.TaskKinds[ai.TaskQueryLogSearch] {
		return nil, apiError{status: http.StatusServiceUnavailable, code: "feature_disabled", msg: "the AI feature querylog_search is disabled"}
	}
	query := strings.TrimSpace(req.Body.Query)
	if n := utf8.RuneCountInString(query); n < 1 || n > maxQueryLogQuestion {
		return nil, invalid("query must be 1 to %d characters", maxQueryLogQuestion)
	}
	task, err := h.startTask(ctx, ai.TaskQueryLogSearch, qlsearch.Input{Query: query, From: req.Body.From, To: req.Body.To})
	if err != nil {
		return nil, err
	}
	return StartAiQueryLogSearch202JSONResponse(task), nil
}
