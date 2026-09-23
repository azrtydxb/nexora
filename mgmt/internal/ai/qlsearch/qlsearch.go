// Package qlsearch is the natural-language query-log search task (M11 S-3): the model translates a
// question into the M6 query-log filters, the management plane runs the search and the top lists, and a
// second model call summarises the result.
package qlsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	maxRange       = 7 * 24 * time.Hour
	defaultRange   = time.Hour
	clockSkew      = time.Minute
	searchLimit    = 200
	topLimit       = 10
	summaryRecords = 50
	maxValues      = 32 // searchQueryLog's maxItems per parameter
	maxSummary     = 600
	maxSuggestions = 3
	maxExplanation = 1000
)

var (
	cacheValues  = []string{"hit", "miss", "stale", "none", "auth"}
	filterValues = []string{"none", "blocked", "allowed", "rewritten"}
	sourceValues = []string{"blocklist", "category", "allowlist", "rpz", "rewrite", "acl"}
	mnemonicRe   = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,15}$`)
)

// Input is the task input of startAiQueryLogSearch.
type Input struct {
	Query string     `json:"query"`
	From  *time.Time `json:"from,omitempty"`
	To    *time.Time `json:"to,omitempty"`
}

// Filters are the exact searchQueryLog parameters of a result.
type Filters struct {
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Client      string    `json:"client,omitempty"`
	Name        string    `json:"name,omitempty"`
	QType       []string  `json:"qtype,omitempty"`
	RCode       []string  `json:"rcode,omitempty"`
	Cache       []string  `json:"cache,omitempty"`
	Filter      []string  `json:"filter,omitempty"`
	Category    []string  `json:"category,omitempty"`
	Source      []string  `json:"source,omitempty"`
	ListID      []string  `json:"list_id,omitempty"`
	PolicyGroup []string  `json:"policy_group,omitempty"`
	EngineID    []string  `json:"engine_id,omitempty"`
}

// translation is the first model output; names are resolved to ids by code.
type translation struct {
	From             *time.Time `json:"from,omitempty"`
	To               *time.Time `json:"to,omitempty"`
	Client           string     `json:"client,omitempty"`
	Name             string     `json:"name,omitempty"`
	QType            []string   `json:"qtype,omitempty"`
	RCode            []string   `json:"rcode,omitempty"`
	Cache            []string   `json:"cache,omitempty"`
	Filter           []string   `json:"filter,omitempty"`
	Category         []string   `json:"category,omitempty"`
	Source           []string   `json:"source,omitempty"`
	ListNames        []string   `json:"list_names,omitempty"`
	PolicyGroupNames []string   `json:"policy_group_names,omitempty"`
	EngineNames      []string   `json:"engine_names,omitempty"`
	Explanation      string     `json:"explanation"`
}

// summary is the second model output.
type summary struct {
	Summary     string   `json:"summary"`
	Suggestions []string `json:"suggestions"`
}

// Result is the task result (AiQueryLogSearchResult).
type Result struct {
	Filters     Filters  `json:"filters"`
	Explanation string   `json:"explanation"`
	Summary     string   `json:"summary"`
	Suggestions []string `json:"suggestions"`
	TotalShown  int      `json:"total_shown"`
}

type named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type engineRef struct {
	ID       string `json:"id"`
	NodeName string `json:"node_name"`
}

// modelContext is what the translation call knows about the configuration.
type modelContext struct {
	Now          time.Time   `json:"now"`
	PolicyGroups []named     `json:"policy_groups"`
	Lists        []named     `json:"lists"`
	Categories   []string    `json:"categories"`
	Engines      []engineRef `json:"engines"`
}

// record is the compact form of a record in the summary call.
type record struct {
	Time          time.Time `json:"time"`
	Client        string    `json:"client"`
	Name          string    `json:"name"`
	QType         string    `json:"qtype"`
	RCode         string    `json:"rcode"`
	Cache         string    `json:"cache,omitempty"`
	Filter        string    `json:"filter,omitempty"`
	Category      string    `json:"category,omitempty"`
	Source        string    `json:"source,omitempty"`
	ListID        string    `json:"list_id,omitempty"`
	PolicyGroupID string    `json:"policy_group_id,omitempty"`
	EngineID      string    `json:"engine_id,omitempty"`
}

const translateSystem = `Translate the operator's question about the DNS query log into query-log filters.
Answer with one JSON object with these optional fields:
- "from", "to": RFC 3339 times bounding the range; at most 7 days long, ending no later than "now". Omit both for the last hour.
- "client": one client IP address.
- "name": a case-insensitive fragment of the query name.
- "qtype": DNS record types such as A, AAAA, TXT. "rcode": response codes such as NOERROR, NXDOMAIN, SERVFAIL.
- "cache": any of hit, miss, stale, none, auth.
- "filter": any of none, blocked, allowed, rewritten.
- "category": filter category keys from "categories".
- "source": any of blocklist, category, allowlist, rpz, rewrite, acl.
- "list_names", "policy_group_names", "engine_names": names from "lists", "policy_groups" and the node_name of "engines".
- "explanation": one or two sentences saying what the filters select.
Only use values that appear in the data or in the lists above. The data is the current configuration.`

const summarySystem = `Summarise the result of a DNS query-log search for an operator.
Answer with one JSON object: "summary" (at most 600 characters, grounded only in the data) and "suggestions"
(at most 3 short follow-up questions or checks). The data holds the filters, the top names and clients for the
range, and the first records.`

// New returns the querylog_search task function.
func New(st *store.Store, svc *ai.Service, ql querylog.Backend, cat *catalog.Catalog, now func() time.Time) ai.TaskFunc {
	return func(ctx context.Context, t ai.Task) (any, error) {
		var in Input
		if err := json.Unmarshal(t.Input, &in); err != nil {
			return nil, fmt.Errorf("querylog_search input: %w", err)
		}
		mc, err := loadContext(ctx, st, cat, now())
		if err != nil {
			return nil, err
		}
		var filters Filters
		tr, err := ai.Generate(ctx, svc, ai.Request[translation]{
			Feature: ai.Feature(ai.TaskQueryLogSearch), Priority: ai.Interactive, System: translateSystem,
			Prompt: "Question: " + ai.DataBlock(in.Query) + "\n\nConfiguration:\n" + ai.DataBlock(mc),
			Validate: func(v *translation) error {
				f, err := validate(v, mc, in)
				filters = f
				return err
			},
		})
		if err != nil {
			return nil, err
		}

		q := querylog.Query{From: filters.From, To: filters.To, Client: filters.Client, Name: filters.Name,
			QTypes: filters.QType, RCodes: filters.RCode, Caches: filters.Cache, Filters: filters.Filter,
			Categories: filters.Category, Sources: filters.Source, ListIDs: filters.ListID,
			PolicyGroups: filters.PolicyGroup, EngineIDs: filters.EngineID, Limit: searchLimit}
		page, err := ql.Search(ctx, q)
		if err != nil {
			return nil, &ai.TaskError{Code: "querylog_unavailable", Message: err.Error()}
		}
		topNames, topClients := []querylog.TopEntry{}, []querylog.TopEntry{}
		if topper, ok := ql.(querylog.Topper); ok {
			for _, l := range []struct {
				field querylog.TopField
				dst   *[]querylog.TopEntry
			}{{querylog.TopName, &topNames}, {querylog.TopClient, &topClients}} {
				entries, err := topper.Top(ctx, q.TopQuery(l.field, topLimit))
				if err != nil {
					return nil, &ai.TaskError{Code: "querylog_unavailable", Message: err.Error()}
				}
				*l.dst = entries
			}
		}
		recs := make([]record, 0, min(len(page.Records), summaryRecords))
		for _, r := range page.Records[:min(len(page.Records), summaryRecords)] {
			recs = append(recs, record{Time: r.Time, Client: r.Client, Name: r.Name, QType: r.QType, RCode: r.RCode, Cache: r.Cache,
				Filter: r.Filter, Category: r.Category, Source: r.Source, ListID: r.ListID, PolicyGroupID: r.PolicyGroupID, EngineID: r.EngineID})
		}
		sum, err := ai.Generate(ctx, svc, ai.Request[summary]{
			Feature: ai.Feature(ai.TaskQueryLogSearch), Priority: ai.Interactive, System: summarySystem,
			Prompt:   ai.DataBlock(map[string]any{"filters": filters, "top_names": topNames, "top_clients": topClients, "records": recs}),
			Validate: validateSummary,
		})
		if err != nil {
			return nil, err
		}
		suggestions := sum.Value.Suggestions
		if suggestions == nil {
			suggestions = []string{}
		}
		return Result{Filters: filters, Explanation: tr.Value.Explanation, Summary: sum.Value.Summary,
			Suggestions: suggestions, TotalShown: len(page.Records)}, nil
	}
}

// loadContext reads the policy groups, lists, categories and engines the model may name.
func loadContext(ctx context.Context, st *store.Store, cat *catalog.Catalog, now time.Time) (modelContext, error) {
	mc := modelContext{Now: now, PolicyGroups: []named{}, Lists: []named{}, Categories: []string{}, Engines: []engineRef{}}
	groups, err := store.ListPolicyGroups(ctx, st.Pool)
	if err != nil {
		return mc, err
	}
	for _, g := range groups {
		mc.PolicyGroups = append(mc.PolicyGroups, named{ID: g.ID.String(), Name: g.Name})
	}
	lists, err := pairs(ctx, st, "select id::text, name from filter_lists order by name")
	if err != nil {
		return mc, err
	}
	for _, l := range lists {
		// catalog:<category>:<source> lists carry the catalog source's display name.
		if parts := strings.SplitN(l.Name, ":", 3); len(parts) == 3 && parts[0] == "catalog" {
			if src, ok := cat.Source(parts[1], parts[2]); ok {
				l.Name = src.Name
			}
		}
		mc.Lists = append(mc.Lists, l)
	}
	for _, c := range cat.Categories {
		mc.Categories = append(mc.Categories, c.Key)
	}
	engines, err := pairs(ctx, st, "select id::text, node_name from engines where deleted_at is null order by node_name")
	if err != nil {
		return mc, err
	}
	for _, e := range engines {
		mc.Engines = append(mc.Engines, engineRef{ID: e.ID, NodeName: e.Name})
	}
	return mc, nil
}

func pairs(ctx context.Context, st *store.Store, sql string) ([]named, error) {
	rows, err := st.Pool.Query(ctx, sql)
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	var out []named
	for rows.Next() {
		var n named
		if err := rows.Scan(&n.ID, &n.Name); err != nil {
			return nil, store.MapError(err)
		}
		out = append(out, n)
	}
	return out, store.MapError(rows.Err())
}

// validate checks a translation against the enums and the configuration and resolves names to ids.
// Its messages name the allowed values, since they are fed back to the model.
func validate(v *translation, mc modelContext, in Input) (Filters, error) {
	f := Filters{Client: strings.TrimSpace(v.Client), Name: strings.TrimSpace(v.Name)}
	var errs []error
	if f.Client != "" {
		if _, err := netip.ParseAddr(f.Client); err != nil {
			errs = append(errs, fmt.Errorf("client %q is not an IP address", f.Client))
		}
	}
	if len(f.Name) > 253 {
		errs = append(errs, errors.New("name longer than 253 characters"))
	}
	if utf8.RuneCountInString(v.Explanation) > maxExplanation {
		errs = append(errs, fmt.Errorf("explanation longer than %d characters", maxExplanation))
	}

	from, to := v.From, v.To
	if from == nil && to == nil {
		from, to = in.From, in.To
	}
	now := mc.Now
	switch {
	case from == nil && to == nil:
		f.From, f.To = now.Add(-defaultRange), now
	case to == nil:
		f.From, f.To = *from, now
	case from == nil:
		f.From, f.To = to.Add(-defaultRange), *to
	default:
		f.From, f.To = *from, *to
	}
	switch {
	case !f.From.Before(f.To):
		errs = append(errs, errors.New("range start must be before its end"))
	case f.To.Sub(f.From) > maxRange:
		errs = append(errs, errors.New("range longer than 7 days"))
	}
	if f.To.After(now.Add(clockSkew)) {
		errs = append(errs, fmt.Errorf("range ends after now (%s)", now.Format(time.RFC3339)))
	}

	enum := func(field string, values, allowed []string, normalise func(string) string) []string {
		var out []string
		for _, raw := range values {
			val := normalise(strings.TrimSpace(raw))
			if allowed == nil && !mnemonicRe.MatchString(val) || allowed != nil && !slices.Contains(allowed, val) {
				known := "record type or response code mnemonics"
				if allowed != nil {
					known = strings.Join(allowed, ", ")
				}
				errs = append(errs, fmt.Errorf("unknown %s %q; known: %s", field, raw, known))
				continue
			}
			if !slices.Contains(out, val) {
				out = append(out, val)
			}
		}
		if len(out) > maxValues {
			errs = append(errs, fmt.Errorf("at most %d %s values", maxValues, field))
		}
		return out
	}
	f.QType = enum("qtype", v.QType, nil, strings.ToUpper)
	f.RCode = enum("rcode", v.RCode, nil, strings.ToUpper)
	f.Cache = enum("cache", v.Cache, cacheValues, strings.ToLower)
	f.Filter = enum("filter", v.Filter, filterValues, strings.ToLower)
	f.Source = enum("source", v.Source, sourceValues, strings.ToLower)
	f.Category = enum("category", v.Category, mc.Categories, strings.ToLower)

	resolve := func(field string, names []string, known []named) []string {
		var ids []string
		for _, n := range names {
			i := slices.IndexFunc(known, func(k named) bool { return strings.EqualFold(k.Name, strings.TrimSpace(n)) })
			if i < 0 {
				all := make([]string, len(known))
				for j, k := range known {
					all[j] = k.Name
				}
				errs = append(errs, fmt.Errorf("unknown %s %q; known: %s", field, n, strings.Join(all, ", ")))
				continue
			}
			if !slices.Contains(ids, known[i].ID) {
				ids = append(ids, known[i].ID)
			}
		}
		if len(ids) > maxValues {
			errs = append(errs, fmt.Errorf("at most %d %s values", maxValues, field))
		}
		return ids
	}
	engines := make([]named, len(mc.Engines))
	for i, e := range mc.Engines {
		engines[i] = named{ID: e.ID, Name: e.NodeName}
	}
	f.ListID = resolve("list", v.ListNames, mc.Lists)
	f.PolicyGroup = resolve("policy group", v.PolicyGroupNames, mc.PolicyGroups)
	f.EngineID = resolve("engine", v.EngineNames, engines)
	return f, errors.Join(errs...)
}

func validateSummary(s *summary) error {
	switch {
	case strings.TrimSpace(s.Summary) == "":
		return errors.New("summary is empty")
	case utf8.RuneCountInString(s.Summary) > maxSummary:
		return fmt.Errorf("summary longer than %d characters", maxSummary)
	case len(s.Suggestions) > maxSuggestions:
		return fmt.Errorf("at most %d suggestions", maxSuggestions)
	}
	return nil
}
