package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxChatBody caps a chat completion request; e2e prompts are far smaller.
const maxChatBody = 8 << 20

// openAIResponse is one scripted chat completion answer.
type openAIResponse struct {
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning"`
	FinishReason     string `json:"finish_reason"`
	Status           int    `json:"status"`
	DelayMS          int    `json:"delay_ms"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	ReasoningTokens  int    `json:"reasoning_tokens"`
}

// openAIRequest is what the fixture records about a chat completion request. It never stores the
// Authorization header value or the message text.
type openAIRequest struct {
	Feature        string
	Model          string
	ResponseFormat string
	Bearer         bool
	MaxTokens      int
	Messages       int
	At             time.Time
}

// openAIServer is a scripted fake OpenAI-compatible chat completions endpoint. Each feature (the
// `nexora-feature: <f>` line the management plane puts first in the system prompt) has its own
// response queue; the last response repeats.
type openAIServer struct {
	mu       sync.Mutex
	scripts  map[string][]openAIResponse
	requests []openAIRequest
}

func runOpenAI(args []string) (func(), string, error) {
	fs := flag.NewFlagSet("openai", flag.ContinueOnError)
	listen := fs.String("listen", "", "listen address")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	if *listen == "" {
		return nil, "", errors.New("--listen is required")
	}
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		return nil, "", err
	}
	srv := &http.Server{Handler: newOpenAIHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(lis) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, "openai=" + lis.Addr().String(), nil
}

func newOpenAIHandler() http.Handler {
	s := &openAIServer{scripts: map[string][]openAIResponse{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("PUT /control/script/{feature}", s.script)
	mux.HandleFunc("GET /control/requests", s.listRequests)
	mux.HandleFunc("POST /control/reset", s.reset)
	return mux
}

func (s *openAIServer) chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBody)).Decode(&req); err != nil {
		openAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	rec := openAIRequest{Model: req.Model, Messages: len(req.Messages), MaxTokens: req.MaxTokens, At: time.Now().UTC(),
		Bearer: strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")}
	if rec.MaxTokens == 0 {
		rec.MaxTokens = req.MaxCompletionTokens
	}
	if req.ResponseFormat != nil {
		rec.ResponseFormat = req.ResponseFormat.Type
	}
	for _, m := range req.Messages {
		if m.Role == "system" {
			rec.Feature = featureOf(messageText(m.Content))
			break
		}
	}

	s.mu.Lock()
	s.requests = append(s.requests, rec)
	queue := s.scripts[rec.Feature]
	var resp openAIResponse
	ok := len(queue) > 0
	if ok {
		resp = queue[0]
		if len(queue) > 1 {
			s.scripts[rec.Feature] = queue[1:]
		}
	}
	s.mu.Unlock()

	if !ok {
		openAIError(w, http.StatusInternalServerError, "no script for feature "+rec.Feature)
		return
	}
	if resp.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(resp.DelayMS) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	if resp.Status != 0 && resp.Status != http.StatusOK {
		openAIError(w, resp.Status, resp.Content)
		return
	}
	finish := resp.FinishReason
	if finish == "" {
		finish = "stop"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     "chatcmpl-fake",
		"object": "chat.completion",
		"model":  req.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": resp.Content, "reasoning_content": resp.Reasoning},
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":             resp.PromptTokens,
			"completion_tokens":         resp.CompletionTokens,
			"total_tokens":              resp.PromptTokens + resp.CompletionTokens,
			"completion_tokens_details": map[string]any{"reasoning_tokens": resp.ReasoningTokens},
		},
	})
}

// messageText returns a message's text: a plain string, or the joined text parts of a content array.
func messageText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// featureOf returns the text after `nexora-feature: ` on the first line of a system prompt.
func featureOf(system string) string {
	first, _, _ := strings.Cut(system, "\n")
	f, ok := strings.CutPrefix(strings.TrimSpace(first), "nexora-feature: ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(f)
}

func (s *openAIServer) script(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Responses []openAIResponse `json:"responses"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBody)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if len(body.Responses) == 0 {
		delete(s.scripts, r.PathValue("feature"))
	} else {
		s.scripts[r.PathValue("feature")] = body.Responses
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *openAIServer) listRequests(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := append([]openAIRequest{}, s.requests...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *openAIServer) reset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.scripts = map[string][]openAIResponse{}
	s.requests = nil
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func openAIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}
