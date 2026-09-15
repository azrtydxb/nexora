package filterrec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Name is the agent, proposal source and feature label.
const Name = "filter_recommendations"

const (
	maxRecommendations = 10
	maxDomains         = 50
	maxTitle           = 120
	maxDescription     = 1000
	window             = 24 * time.Hour
	// blockReason and blockConfidence are fixed by code so a reworded answer keeps the proposal fingerprint.
	blockReason     = "filter recommendation"
	blockConfidence = 0.8
)

// recommendation is one item of the model output. The model never supplies impact numbers: any it sends
// are ignored.
type recommendation struct {
	Kind          string           `json:"kind"`                   // enable_category|block_domains|allow_domains|safe_search
	PolicyGroupID string           `json:"policy_group_id"`        // "" = global
	CategoryKey   string           `json:"category_key,omitempty"` // enable_category
	Domains       []string         `json:"domains,omitempty"`      // block_domains, allow_domains; at most 50
	SafeSearch    *SafeSearchState `json:"safe_search,omitempty"`  // safe_search: the full new selection
	Priority      string           `json:"priority"`               // low|medium|high
	Title         string           `json:"title"`
	Description   string           `json:"description"`
}

type answer struct {
	Recommendations []recommendation `json:"recommendations"`
}

// Agent is the filter_recommendations background agent.
type Agent struct {
	Store     *store.Store
	Service   *ai.Service
	QueryLog  querylog.Backend
	Catalog   *catalog.Catalog
	Validator *proposal.Validator
	Now       func() time.Time
}

// Name implements ai.Agent.
func (a *Agent) Name() string { return Name }

const system = `You review 24 hours of DNS traffic statistics per policy group ("" is the global scope for clients in no group) and recommend at most 10 filtering changes, most valuable first. Recommend nothing when nothing is worth changing.
Answer only JSON: {"recommendations":[{"kind","policy_group_id","category_key","domains","safe_search","priority","title","description"}]}.
Kinds:
- enable_category: turn on a catalog category (category_key) that is disabled for the scope; prefer categories with disabled_category_hits.
- block_domains: block unwanted names (domains, at most 50) for every client through the AI RPZ zone; policy_group_id is ignored.
- allow_domains: allowlist wrongly blocked names (domains) for the scope.
- safe_search: set the scope's full safe-search selection (safe_search {google, bing, duckduckgo, youtube: off|moderate|strict}).
priority is low, medium or high; title at most 120 characters; description at most 1,000 characters explaining why. Do not compute impact numbers: the system computes them.`

// Run aggregates the query log, asks the model for recommendations and stores each as a proposal.
func (a *Agent) Run(ctx context.Context, run *ai.Run) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	stats, err := Aggregate(ctx, a.Store, a.QueryLog, a.Catalog, now.Add(-window), now)
	if err != nil {
		return err
	}
	var records int64
	for _, s := range stats {
		records += s.Total
	}
	if records == 0 {
		run.Outcome, run.Detail = "no_change", map[string]any{"records": 0}
		return nil
	}
	env, err := a.load(ctx, stats)
	if err != nil {
		return err
	}
	var drafts []proposal.Draft
	res, err := ai.Generate(ctx, a.Service, ai.Request[answer]{
		Feature: Name, Priority: ai.Background, System: system,
		Prompt: "Configuration and traffic statistics:\n" + ai.DataBlock(env.promptData()),
		Validate: func(ans *answer) error {
			d, err := a.drafts(ctx, env, ans.Recommendations)
			if err == nil {
				drafts = d
			}
			return err
		},
	})
	run.Usage = res.Usage
	if errors.Is(err, ai.ErrBudgetExhausted) {
		run.Outcome = "skipped_budget"
		return nil
	}
	if err != nil {
		return err
	}
	created, refreshed, suppressed := 0, 0, 0
	for _, d := range drafts {
		id, isNew, err := proposal.Upsert(ctx, a.Store, d)
		switch {
		case err != nil:
			return fmt.Errorf("store proposal %q: %w", d.Title, err)
		case id == uuid.Nil:
			suppressed++
		case isNew:
			created++
		default:
			refreshed++
		}
	}
	run.Detail = map[string]any{"records": records, "recommendations": len(drafts), "created": created, "refreshed": refreshed,
		"suppressed_dismissed": suppressed}
	if created == 0 && refreshed == 0 {
		run.Outcome = "no_change"
	}
	return nil
}

// env is the configuration a run's recommendations are checked and turned into actions against.
type env struct {
	stats      []GroupStats
	groups     map[string]store.PolicyGroup
	categories map[string]store.FilterCategory
	global     store.GlobalSafeSearch
	allowlist  []string
	allowRev   int64
	catalog    *catalog.Catalog
}

func (a *Agent) load(ctx context.Context, stats []GroupStats) (*env, error) {
	e := &env{stats: stats, groups: map[string]store.PolicyGroup{}, categories: map[string]store.FilterCategory{}, catalog: a.Catalog}
	groups, err := store.ListPolicyGroups(ctx, a.Store.Pool)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		e.groups[g.ID.String()] = g
	}
	cats, err := store.ListFilterCategories(ctx, a.Store.Pool)
	if err != nil {
		return nil, err
	}
	for _, c := range cats {
		e.categories[c.Key] = c
	}
	if e.global, err = store.GetGlobalSafeSearch(ctx, a.Store.Pool); err != nil {
		return nil, err
	}
	if err := a.Store.Pool.QueryRow(ctx, "select domains, revision from allowlist").Scan(&e.allowlist, &e.allowRev); err != nil {
		return nil, store.MapError(err)
	}
	return e, nil
}

func (e *env) promptData() any {
	type group struct {
		ID           string          `json:"policy_group_id"`
		Name         string          `json:"name"`
		CategoryKeys []string        `json:"category_keys"`
		SafeSearch   SafeSearchState `json:"safe_search"`
	}
	type category struct {
		Key             string `json:"key"`
		Name            string `json:"name"`
		EnabledGlobally bool   `json:"enabled_globally"`
	}
	groups := []group{{ID: "", Name: "global", CategoryKeys: []string{}, SafeSearch: stateOf(e.global.SafeSearch)}}
	for _, g := range e.groups {
		groups = append(groups, group{ID: g.ID.String(), Name: g.Name, CategoryKeys: g.CategoryKeys, SafeSearch: stateOf(g.SafeSearch)})
	}
	slices.SortFunc(groups, func(a, b group) int { return strings.Compare(a.ID, b.ID) })
	var cats []category
	for key, c := range e.categories {
		name := key
		if e.catalog != nil {
			if cc, ok := e.catalog.Category(key); ok {
				name = cc.Name
			}
		}
		cats = append(cats, category{Key: key, Name: name, EnabledGlobally: c.Enabled})
	}
	slices.SortFunc(cats, func(a, b category) int { return strings.Compare(a.Key, b.Key) })
	return map[string]any{"policy_groups": groups, "filter_categories": cats, "statistics": e.stats}
}

// scope sums the statistics of one policy group ("" = global) over its engine groups, or of everything
// when all is set.
type scope struct {
	total     int64
	unblocked map[string]int64
	blocked   map[string]int64
	search    map[string]int64
}

func (e *env) scope(groupID string, all bool) scope {
	s := scope{unblocked: map[string]int64{}, blocked: map[string]int64{}, search: map[string]int64{}}
	for _, gs := range e.stats {
		if !all && gs.PolicyGroupID != groupID {
			continue
		}
		s.total += gs.Total
		for n, acc := range gs.unblocked {
			s.unblocked[n] += acc.queries
		}
		for n, c := range gs.blocked {
			s.blocked[n] += c
		}
		for f, c := range gs.SearchHosts {
			s.search[f] += c
		}
	}
	return s
}

func (e *env) categoryHits(groupID, key string) int64 {
	var n int64
	for _, gs := range e.stats {
		if gs.PolicyGroupID == groupID {
			n += gs.DisabledCategoryHits[key]
		}
	}
	return n
}

func impact(x, total int64) map[string]any {
	pct := 0.0
	if total > 0 {
		pct = math.Round(1000*float64(x)/float64(total)) / 10
	}
	return map[string]any{"additional_blocked_queries": x, "total_queries_analyzed": total, "coverage_percent": pct}
}

// drafts turns the recommendations into validated proposal drafts; the first problem is returned so the
// model can correct its answer.
func (a *Agent) drafts(ctx context.Context, e *env, recs []recommendation) ([]proposal.Draft, error) {
	if len(recs) > maxRecommendations {
		return nil, fmt.Errorf("at most %d recommendations, got %d", maxRecommendations, len(recs))
	}
	out := make([]proposal.Draft, 0, len(recs))
	for i, r := range recs {
		d, err := a.draft(ctx, e, r)
		if err != nil {
			return nil, fmt.Errorf("recommendations[%d] (%s): %w", i, r.Kind, err)
		}
		out = append(out, d)
	}
	return out, nil
}

func (a *Agent) draft(ctx context.Context, e *env, r recommendation) (proposal.Draft, error) {
	switch {
	case r.Priority != "low" && r.Priority != "medium" && r.Priority != "high":
		return proposal.Draft{}, fmt.Errorf("priority %q must be low, medium or high", r.Priority)
	case strings.TrimSpace(r.Title) == "" || utf8.RuneCountInString(r.Title) > maxTitle:
		return proposal.Draft{}, fmt.Errorf("title needs 1 to %d characters", maxTitle)
	case utf8.RuneCountInString(r.Description) > maxDescription:
		return proposal.Draft{}, fmt.Errorf("description has more than %d characters", maxDescription)
	}
	var group *store.PolicyGroup
	if r.PolicyGroupID != "" && r.Kind != "block_domains" {
		g, ok := e.groups[r.PolicyGroupID]
		if !ok {
			return proposal.Draft{}, fmt.Errorf("unknown policy_group_id %q", r.PolicyGroupID)
		}
		group = &g
	}
	evidence := map[string]any{"kind": r.Kind, "policy_group_id": r.PolicyGroupID}
	if group != nil {
		evidence["policy_group_name"] = group.Name
	}
	var action proposal.Action
	var x, total int64
	var err error
	switch r.Kind {
	case "enable_category":
		action, err = e.enableCategory(group, r.CategoryKey)
		x, total = e.categoryHits(r.PolicyGroupID, r.CategoryKey), e.scope(r.PolicyGroupID, false).total
		evidence["category_key"] = r.CategoryKey
		if notice := e.licenseNotice(r.CategoryKey); len(notice) > 0 {
			evidence["license_notice_sources"] = notice
		}
	case "block_domains":
		var domains []string
		if domains, err = normalDomains(r.Domains); err == nil {
			action, err = blockAction(domains)
			s := e.scope("", true)
			x, total = matchCount(s.unblocked, domains, matchesRecord), s.total
			evidence["domains"], evidence["policy_group_id"] = domains, ""
		}
	case "allow_domains":
		var domains []string
		if domains, err = normalDomains(r.Domains); err == nil {
			action, err = e.allowAction(group, domains)
			s := e.scope(r.PolicyGroupID, false)
			x, total = -matchCount(s.blocked, domains, atOrUnder), s.total
			evidence["domains"] = domains
		}
	case "safe_search":
		var turnedOn []string
		action, turnedOn, err = e.safeSearchAction(group, r.SafeSearch)
		s := e.scope(r.PolicyGroupID, false)
		for _, f := range turnedOn {
			x += s.search[f]
		}
		total = s.total
		evidence["safe_search"] = r.SafeSearch
	default:
		err = fmt.Errorf("kind %q must be enable_category, block_domains, allow_domains or safe_search", r.Kind)
	}
	if err != nil {
		return proposal.Draft{}, err
	}
	actions := []proposal.Action{action}
	if err := a.Validator.Validate(ctx, actions); err != nil {
		return proposal.Draft{}, err
	}
	return proposal.Draft{Source: Name, Title: r.Title, Description: r.Description, Priority: r.Priority,
		Impact: impact(x, total), Evidence: evidence, Actions: actions}, nil
}

func (e *env) enableCategory(group *store.PolicyGroup, key string) (proposal.Action, error) {
	c, ok := e.categories[key]
	if !ok {
		return proposal.Action{}, fmt.Errorf("unknown category_key %q", key)
	}
	if c.Enabled {
		return proposal.Action{}, fmt.Errorf("category %s is already enabled globally", key)
	}
	if group == nil {
		return action("updateFilterCategory", map[string]string{"key": key}, map[string]any{"enabled": true, "revision": c.Revision})
	}
	if slices.Contains(group.CategoryKeys, key) {
		return proposal.Action{}, fmt.Errorf("policy group %s already blocks category %s", group.Name, key)
	}
	body := groupBody(*group)
	keys := append(slices.Clone(group.CategoryKeys), key)
	slices.Sort(keys)
	body["category_keys"] = keys
	return action("updatePolicyGroup", map[string]string{"id": group.ID.String()}, body)
}

// licenseNotice names the category's sources that are not free for commercial use.
func (e *env) licenseNotice(key string) []string {
	if e.catalog == nil {
		return nil
	}
	c, ok := e.catalog.Category(key)
	if !ok {
		return nil
	}
	var out []string
	for _, s := range c.Sources {
		if !s.CommercialUse {
			out = append(out, s.Name)
		}
	}
	return out
}

func (e *env) allowAction(group *store.PolicyGroup, domains []string) (proposal.Action, error) {
	current := e.allowlist
	if group != nil {
		current = group.Allowlist
	}
	merged := slices.Clone(current)
	for _, d := range domains {
		if !slices.Contains(merged, d) {
			merged = append(merged, d)
		}
	}
	if len(merged) == len(current) {
		return proposal.Action{}, errors.New("every domain is already on the allowlist")
	}
	slices.Sort(merged)
	if group == nil {
		return action("updateAllowlist", nil, map[string]any{"domains": merged, "revision": e.allowRev})
	}
	body := groupBody(*group)
	body["allowlist"] = merged
	return action("updatePolicyGroup", map[string]string{"id": group.ID.String()}, body)
}

// safeSearchAction returns the action and the host families the change turns on.
func (e *env) safeSearchAction(group *store.PolicyGroup, want *SafeSearchState) (proposal.Action, []string, error) {
	if want == nil {
		return proposal.Action{}, nil, errors.New("safe_search is required")
	}
	if want.YouTube != "off" && want.YouTube != "moderate" && want.YouTube != "strict" {
		return proposal.Action{}, nil, fmt.Errorf("safe_search.youtube %q must be off, moderate or strict", want.YouTube)
	}
	cur := stateOf(e.global.SafeSearch)
	if group != nil {
		cur = stateOf(group.SafeSearch)
	}
	if cur == *want {
		return proposal.Action{}, nil, errors.New("safe_search equals the current selection")
	}
	var on []string
	for _, f := range []struct {
		family   string
		was, now bool
	}{
		{"google", cur.Google, want.Google}, {"bing", cur.Bing, want.Bing}, {"duckduckgo", cur.DuckDuckGo, want.DuckDuckGo},
		{"youtube", cur.YouTube != "off", want.YouTube != "off"},
	} {
		if f.now && !f.was {
			on = append(on, f.family)
		}
	}
	ss := map[string]any{"google": want.Google, "bing": want.Bing, "duckduckgo": want.DuckDuckGo, "youtube": want.YouTube}
	if group == nil {
		ss["revision"] = e.global.Revision
		a, err := action("updateGlobalSafeSearch", nil, ss)
		return a, on, err
	}
	body := groupBody(*group)
	body["safe_search"] = ss
	a, err := action("updatePolicyGroup", map[string]string{"id": group.ID.String()}, body)
	return a, on, err
}

// groupBody is the updatePolicyGroup body that keeps the group as it is.
func groupBody(g store.PolicyGroup) map[string]any {
	cidrs := make([]string, len(g.CIDRs))
	for i, c := range g.CIDRs {
		cidrs[i] = c.String()
	}
	lists := make([]string, len(g.FilterListIDs))
	for i, id := range g.FilterListIDs {
		lists[i] = id.String()
	}
	body := map[string]any{"name": g.Name, "description": g.Description, "cidrs": cidrs, "filter_list_ids": lists,
		"allowlist": nonNil(g.Allowlist), "category_keys": nonNil(g.CategoryKeys), "revision": g.Revision,
		"safe_search": map[string]any{"google": g.SafeSearch.Google, "bing": g.SafeSearch.Bing, "duckduckgo": g.SafeSearch.DuckDuckGo,
			"youtube": g.SafeSearch.YouTube}}
	if g.EngineGroupID != nil {
		body["engine_group_id"] = g.EngineGroupID.String()
	}
	return body
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func blockAction(domains []string) (proposal.Action, error) {
	rules := make([]proposal.RPZRule, len(domains))
	for i, d := range domains {
		rules[i] = proposal.RPZRule{Record: d, Policy: "nxdomain", Category: "custom", Reason: blockReason, Confidence: blockConfidence}
	}
	return action(proposal.OpAppendAiRpzRules, nil, map[string]any{"rules": rules})
}

func action(op string, params map[string]string, body any) (proposal.Action, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return proposal.Action{}, err
	}
	if params == nil {
		params = map[string]string{}
	}
	return proposal.Action{OperationID: op, PathParams: params, Body: raw}, nil
}

// normalDomains lower-cases, trims, de-duplicates and sorts 1 to 50 domains; name syntax is left to the
// proposal validator.
func normalDomains(in []string) ([]string, error) {
	var out []string
	for _, d := range in {
		if d = normalName(d); d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	if len(out) == 0 || len(out) > maxDomains {
		return nil, fmt.Errorf("domains needs 1 to %d names, got %d", maxDomains, len(out))
	}
	slices.Sort(out)
	return out, nil
}

// matchesRecord reports whether an RPZ record (exact, or *.parent for names under parent) matches name.
func matchesRecord(name, record string) bool {
	if base, ok := strings.CutPrefix(record, "*."); ok {
		return name != base && atOrUnder(name, base)
	}
	return name == record
}

// matchCount sums the counts of the names any domain matches.
func matchCount(names map[string]int64, domains []string, match func(name, domain string) bool) int64 {
	var n int64
	for name, c := range names {
		if slices.ContainsFunc(domains, func(d string) bool { return match(name, d) }) {
			n += c
		}
	}
	return n
}
