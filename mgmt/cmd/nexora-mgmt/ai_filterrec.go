package main

import (
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/filterrec"
)

func init() {
	registerAIAgent(func(d aiDeps) ai.Agent {
		return &filterrec.Agent{Store: d.Store, Service: d.Service, QueryLog: d.QueryLog, Catalog: d.Catalog, Validator: d.Validator}
	})
}
