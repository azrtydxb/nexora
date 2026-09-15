package main

import (
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/assistant"
)

func init() {
	registerAITask(ai.TaskAssistantMessage, func(d aiDeps) ai.TaskFunc {
		return assistant.NewTask(d.Store, d.Service, d.Validator, time.Now)
	})
}
