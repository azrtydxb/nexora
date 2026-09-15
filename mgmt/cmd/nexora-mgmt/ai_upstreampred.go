package main

import (
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/upstreampred"
)

func init() {
	registerAIAgent(func(d aiDeps) ai.Agent {
		return &upstreampred.Agent{Store: d.Store, Service: d.Service, Validator: d.Validator,
			Interval: d.Cfg.AI.Agents[upstreampred.Name].Interval}
	})
}
