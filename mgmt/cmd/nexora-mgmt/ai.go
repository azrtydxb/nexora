package main

import (
	"context"
	"log"
	"log/slog"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// aiDeps are the collaborators an AI agent or task kind is built from. startAI fills Service and Tasks.
type aiDeps struct {
	Cfg        config.Config
	Store      *store.Store
	Service    *ai.Service
	Tasks      *ai.Tasks
	QueryLog   querylog.Backend
	Catalog    *catalog.Catalog
	Build      snapshot.BuildConfig
	Validator  *proposal.Validator
	InstanceID string
}

type aiTaskFactory struct {
	kind ai.TaskKind
	f    func(aiDeps) ai.TaskFunc
}

// The registries are filled by the init functions of the ai_<feature>.go files, before main runs.
var (
	aiAgentFactories []func(aiDeps) ai.Agent
	aiTaskFactories  []aiTaskFactory
)

// registerAIAgent adds a background agent; startAI builds it when its configuration enables it.
func registerAIAgent(f func(aiDeps) ai.Agent) { aiAgentFactories = append(aiAgentFactories, f) }

// registerAITask adds an interactive task kind.
func registerAITask(kind ai.TaskKind, f func(aiDeps) ai.TaskFunc) {
	aiTaskFactories = append(aiTaskFactories, aiTaskFactory{kind, f})
}

// pruneInterval is how often AI rows past their retention are deleted.
const pruneInterval = time.Hour

// startAI returns (nil, reason) when AI is off: no scheduler, no tasks, no collector, no connection.
// Otherwise it starts the agent scheduler and the pruning, registers the state collector and returns the
// handlers' runtime.
func startAI(ctx context.Context, d aiDeps, reg prometheus.Registerer) (*api.AIRuntime, string, error) {
	svc, reason, err := ai.New(ctx, ai.Options{Config: d.Cfg.AI, Store: d.Store, Registerer: reg})
	if err != nil {
		return nil, "", err
	}
	if svc == nil {
		ai.Enabled.Set(0)
		log.Printf("AI: off (%s)", reason)
		return nil, reason, nil
	}
	d.Service = svc
	d.Tasks = ai.NewTasks(ctx, d.Store, d.InstanceID)
	c := d.Cfg.AI

	rt := &api.AIRuntime{Service: svc, Tasks: d.Tasks, Proposals: d.Validator, Config: c, InstanceStart: time.Now(),
		TaskKinds: map[ai.TaskKind]bool{}, Agents: map[string]bool{}}
	var agents []ai.Agent
	for _, f := range aiAgentFactories {
		a := f(d)
		if c.Agents[a.Name()].Enabled {
			agents = append(agents, a)
			rt.Agents[a.Name()] = true
		}
	}
	for _, t := range aiTaskFactories {
		if t.kind == ai.TaskAssistantMessage && !c.ConfigAssistantEnabled {
			continue
		}
		d.Tasks.Register(t.kind, t.f(d))
		rt.TaskKinds[t.kind] = true
	}

	go func() {
		_ = (&ai.Scheduler{Store: d.Store, InstanceID: d.InstanceID, Agents: agents, Config: c}).Run(ctx)
	}()
	go runAIPrune(ctx, d.Store)
	if err := reg.Register(&aiStateCollector{st: d.Store, svc: svc}); err != nil {
		return nil, "", err
	}
	ai.Enabled.Set(1)
	host := ""
	if u, err := url.Parse(c.BaseURL); err == nil {
		host = u.Hostname()
	}
	log.Printf("AI: on (model %s at %s, %d agents)", c.Model, host, len(agents))
	return rt, "", nil
}

func runAIPrune(ctx context.Context, st *store.Store) {
	t := time.NewTicker(pruneInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := ai.Prune(ctx, st, now); err != nil && ctx.Err() == nil {
				slog.Warn("AI prune failed", "err", err)
			}
		}
	}
}

// aiStateCollector reports the open findings, open proposals and today's budget use at scrape time.
type aiStateCollector struct {
	st  *store.Store
	svc *ai.Service
}

var (
	aiOpenFindingsDesc = prometheus.NewDesc("nexora_mgmt_ai_open_findings", "Open AI findings by kind and severity",
		[]string{"kind", "severity"}, nil)
	aiOpenProposalsDesc = prometheus.NewDesc("nexora_mgmt_ai_open_proposals", "Open AI proposals by source", []string{"source"}, nil)
	aiBudgetUsedDesc    = prometheus.NewDesc("nexora_mgmt_ai_budget_used_ratio", "Share of today's AI token budget used", nil, nil)
)

// aiScrapeTimeout bounds the database reads of one scrape.
const aiScrapeTimeout = 5 * time.Second

func (c *aiStateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- aiOpenFindingsDesc
	ch <- aiOpenProposalsDesc
	ch <- aiBudgetUsedDesc
}

func (c *aiStateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), aiScrapeTimeout)
	defer cancel()
	c.counts(ctx, ch, aiOpenFindingsDesc, `select kind, severity, count(*) from ai_findings where status = 'open' group by kind, severity`, 2)
	c.counts(ctx, ch, aiOpenProposalsDesc, `select source, count(*) from ai_proposals where status = 'open' group by source`, 1)
	b, err := c.svc.Budget(ctx)
	if err != nil {
		slog.Warn("AI metrics: budget", "err", err)
		return
	}
	ratio := 0.0
	if b.LimitTokens > 0 {
		ratio = float64(b.UsedTokens) / float64(b.LimitTokens)
	}
	ch <- prometheus.MustNewConstMetric(aiBudgetUsedDesc, prometheus.GaugeValue, ratio)
}

// counts emits one gauge per row of sql, whose first labels columns are the label values and whose last
// column is the count.
func (c *aiStateCollector) counts(ctx context.Context, ch chan<- prometheus.Metric, desc *prometheus.Desc, sql string, labels int) {
	rows, err := c.st.Pool.Query(ctx, sql)
	if err != nil {
		slog.Warn("AI metrics", "metric", desc.String(), "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]string, labels)
		dest := make([]any, labels+1)
		for i := range values {
			dest[i] = &values[i]
		}
		var n int64
		dest[labels] = &n
		if err := rows.Scan(dest...); err != nil {
			slog.Warn("AI metrics", "metric", desc.String(), "err", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(n), values...)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("AI metrics", "metric", desc.String(), "err", err)
	}
}
