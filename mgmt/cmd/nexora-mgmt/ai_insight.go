package main

import (
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/insight"
)

func init() {
	registerAIAgent(func(d aiDeps) ai.Agent {
		return &insight.Agent{Store: d.Store, Service: d.Service, MinLLMInterval: d.Cfg.AI.LLMMinInterval}
	})
}
