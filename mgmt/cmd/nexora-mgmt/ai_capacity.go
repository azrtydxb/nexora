package main

import (
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/capacity"
)

func init() {
	registerAIAgent(func(d aiDeps) ai.Agent {
		return &capacity.Agent{Store: d.Store, Service: d.Service, Validator: d.Validator,
			Interval: d.Cfg.AI.Agents[capacity.AgentName].Interval}
	})
}
