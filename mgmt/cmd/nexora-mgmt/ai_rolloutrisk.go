package main

import (
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/rolloutrisk"
)

func init() {
	registerAIAgent(func(d aiDeps) ai.Agent {
		return &rolloutrisk.Agent{Store: d.Store, Service: d.Service, Validator: d.Validator}
	})
}
