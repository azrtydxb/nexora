package main

import (
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/anomaly"
)

func init() {
	registerAIAgent(func(d aiDeps) ai.Agent {
		return &anomaly.Agent{Store: d.Store, Service: d.Service, QueryLog: d.QueryLog,
			Interval: d.Cfg.AI.Agents[anomaly.AgentName].Interval, MinLLMInterval: d.Cfg.AI.LLMMinInterval}
	})
}
