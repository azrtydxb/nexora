package main

import (
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/rpzsuggest"
)

func init() {
	registerAIAgent(func(d aiDeps) ai.Agent {
		return &rpzsuggest.Agent{Store: d.Store, Service: d.Service, QueryLog: d.QueryLog, Validator: d.Validator}
	})
}
