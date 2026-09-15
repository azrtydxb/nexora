package main

import (
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/qlsearch"
)

func init() {
	registerAITask(ai.TaskQueryLogSearch, func(d aiDeps) ai.TaskFunc {
		return qlsearch.New(d.Store, d.Service, d.QueryLog, d.Catalog, time.Now)
	})
}
