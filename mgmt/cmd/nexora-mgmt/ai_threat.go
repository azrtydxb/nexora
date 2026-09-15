package main

import (
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/threat"
)

func init() {
	registerAITask(ai.TaskThreatCheck, func(d aiDeps) ai.TaskFunc {
		return threat.NewCheckTask(d.Store, d.Service, d.QueryLog, time.Now)
	})
	registerAIAgent(func(d aiDeps) ai.Agent {
		return &threat.ClassifyAgent{Store: d.Store, Service: d.Service}
	})
}
