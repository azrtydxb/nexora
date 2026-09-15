// Package threat is the M11 S-10 threat intelligence assistance: the interactive threat-check task
// with its seven-day verdict cache, and the block list classification agent. Nothing here changes
// configuration: verdicts are advice for an operator and labels on query-log records.
package threat

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

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Categories are the threat categories a verdict may name (M11 S-10).
var Categories = []string{"malware", "phishing", "spam", "c2", "botnet", "adult", "tracking"}

const (
	// MaxDomains is the longest accepted domain list of one check; the handler rejects more with 400.
	MaxDomains = 100
	// checkBatch is how many names one model call judges.
	checkBatch = 20
	// evidenceWindow is how far back the query log is read for a name.
	evidenceWindow = 7 * 24 * time.Hour
	// verdictTTL is how long a verdict answers later checks and labels query-log records.
	verdictTTL = 7 * 24 * time.Hour
	// evidenceLimit caps the records read per name.
	evidenceLimit = 1000
	// maxReasoning is the longest accepted reasoning, in characters.
	maxReasoning = 500
)

// Verdict is one judged domain. It is the AiThreatVerdict shape; CheckedAt is internal.
type Verdict struct {
	Name        string     `json:"name"`
	IsThreat    bool       `json:"is_threat"`
	Categories  []string   `json:"categories"`
	Confidence  float64    `json:"confidence"`
	Reasoning   string     `json:"reasoning"`
	QueryCount  int64      `json:"query_count"`
	ClientCount int64      `json:"client_count"`
	FirstSeen   *time.Time `json:"first_seen"`
	LastSeen    *time.Time `json:"last_seen"`
	BlockedBy   string     `json:"blocked_by"`
	Cached      bool       `json:"cached"`
	CheckedAt   time.Time  `json:"-"`
}

// Input is the task input of startAiThreatCheck.
type Input struct {
	Domains []string `json:"domains"`
}

// Result is the task result (AiThreatCheckResult), in the order the domains were given.
type Result struct {
	Results []Verdict `json:"results"`
}

// Normalize is the cache key of a name: lower-cased, without its trailing dot.
func Normalize(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

// CachedVerdicts returns the non-expired verdicts of names, keyed by normalised name. It is the only
// read on the query-log path, and never calls the model.
func CachedVerdicts(ctx context.Context, q store.PolicyQuerier, names []string, now time.Time) (map[string]Verdict, error) {
	out := map[string]Verdict{}
	keys := make([]string, 0, len(names))
	for _, n := range names {
		if k := Normalize(n); k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `select name, is_threat, categories, confidence, reasoning, checked_at
		from ai_domain_verdicts where name = any($1) and expires_at > $2`, keys, now)
	if err != nil {
		return nil, store.MapError(err)
	}
	var v Verdict
	var confidence float32
	if _, err := pgx.ForEachRow(rows, []any{&v.Name, &v.IsThreat, &v.Categories, &confidence, &v.Reasoning, &v.CheckedAt}, func() error {
		got := v
		got.Confidence = float64(confidence)
		got.Cached = true
		if got.Categories == nil {
			got.Categories = []string{}
		}
		out[got.Name] = got
		return nil
	}); err != nil {
		return nil, store.MapError(err)
	}
	return out, nil
}

// evidence is what code knows about a name before the model judges it.
type evidence struct {
	Name         string     `json:"name"`
	QueryCount   int64      `json:"query_count"`
	ClientCount  int64      `json:"client_count"`
	FirstSeen    *time.Time `json:"first_seen,omitempty"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	BlockedBy    string     `json:"blocked_by,omitempty"`
	Labels       int        `json:"labels"`
	Length       int        `json:"length"`
	TLD          string     `json:"tld"`
	LabelEntropy float64    `json:"label_entropy"`

	listID string // the newest record's list id, resolved to a name before the call
}

type answer struct {
	Name       string   `json:"name"`
	IsThreat   bool     `json:"is_threat"`
	Categories []string `json:"categories"`
	Confidence float64  `json:"confidence"`
	Reasoning  string   `json:"reasoning"`
}

type output struct {
	Verdicts []answer `json:"verdicts"`
}

var system = `You judge whether DNS names are threats, for a DNS resolver operator.
The data block lists the names with what the resolver already knows: how often the fleet's clients queried them
over the last seven days, how many clients did, whether the current filters block or allow them and why, and
lexical features of the name.
Answer with one JSON object:
{"verdicts":[{"name":"<the name, unchanged>","is_threat":true,"categories":["malware"],"confidence":0.0,"reasoning":"<at most 500 characters>"}]}
Give exactly one verdict for every name in the data block and no verdict for any other name.
Categories are any of: ` + categoryList + `. Use an empty list when is_threat is false.
Confidence is between 0 and 1. Judge only from the data and from what you know about the name itself; never claim
to have blocked or changed anything.`

var categoryList = strings.Join(Categories, ", ")

// NewCheckTask returns the threat_check task function: it answers cached names from the cache, collects
// query-log evidence for the rest, judges them in batches and caches the new verdicts for seven days.
func NewCheckTask(st *store.Store, svc *ai.Service, ql querylog.Backend, now func() time.Time) ai.TaskFunc {
	return func(ctx context.Context, t ai.Task) (any, error) {
		var in Input
		if err := json.Unmarshal(t.Input, &in); err != nil {
			return nil, fmt.Errorf("threat_check input: %w", err)
		}
		names := make([]string, 0, len(in.Domains))
		for _, d := range in.Domains {
			if n := Normalize(d); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		if len(names) == 0 || len(names) > MaxDomains {
			return nil, &ai.TaskError{Code: "invalid_input", Message: fmt.Sprintf("a check takes 1 to %d domains", MaxDomains)}
		}
		at := now()

		cached, err := CachedVerdicts(ctx, st.Pool, names, at)
		if err != nil {
			return nil, err
		}
		verdicts := make(map[string]Verdict, len(names))
		var todo []evidence
		for _, n := range names {
			if v, ok := cached[n]; ok {
				verdicts[n] = v
				continue
			}
			ev, err := collect(ctx, ql, n, at)
			if err != nil {
				return nil, err
			}
			todo = append(todo, ev)
		}
		if err := resolveListNames(ctx, st.Pool, todo); err != nil {
			return nil, err
		}

		for batch := range slices.Chunk(todo, checkBatch) {
			res, err := ai.Generate(ctx, svc, ai.Request[output]{
				Feature: ai.Feature(ai.TaskThreatCheck), Priority: ai.Interactive, System: system,
				Prompt:   "Judge these names.\n" + ai.DataBlock(map[string]any{"names": batch}),
				Validate: func(o *output) error { return validate(o, batch) },
			})
			if err != nil {
				return nil, err
			}
			for _, a := range res.Value.Verdicts {
				name := Normalize(a.Name)
				i := slices.IndexFunc(batch, func(e evidence) bool { return e.Name == name })
				v := Verdict{Name: name, IsThreat: a.IsThreat, Categories: a.Categories, Confidence: a.Confidence,
					Reasoning: a.Reasoning, QueryCount: batch[i].QueryCount, ClientCount: batch[i].ClientCount,
					FirstSeen: batch[i].FirstSeen, LastSeen: batch[i].LastSeen, BlockedBy: batch[i].BlockedBy, CheckedAt: at}
				if v.Categories == nil {
					v.Categories = []string{}
				}
				if err := cache(ctx, st, v, at); err != nil {
					return nil, err
				}
				verdicts[name] = v
			}
		}

		out := Result{Results: make([]Verdict, 0, len(names))}
		for _, n := range names {
			out.Results = append(out.Results, verdicts[n])
		}
		return out, nil
	}
}

// collect reads the last seven days of the query log for name and summarises them. The M6 name filter is
// a substring, so only records of exactly this name count.
func collect(ctx context.Context, ql querylog.Backend, name string, now time.Time) (evidence, error) {
	ev := evidence{Name: name, Labels: len(strings.Split(name, ".")), Length: len(name), LabelEntropy: entropy(longestLabel(name))}
	if i := strings.LastIndex(name, "."); i >= 0 {
		ev.TLD = name[i+1:]
	}
	page, err := ql.Search(ctx, querylog.Query{Name: name, From: now.Add(-evidenceWindow), To: now, Limit: evidenceLimit})
	if err != nil {
		return ev, &ai.TaskError{Code: "querylog_unavailable", Message: err.Error()}
	}
	var clients []string
	var newest querylog.Record
	for _, r := range page.Records {
		if !strings.EqualFold(strings.TrimSuffix(r.Name, "."), name) {
			continue
		}
		ev.QueryCount++
		if !slices.Contains(clients, r.Client) {
			clients = append(clients, r.Client)
		}
		at := r.Time
		if ev.FirstSeen == nil || at.Before(*ev.FirstSeen) {
			ev.FirstSeen = &at
		}
		if ev.LastSeen == nil || at.After(*ev.LastSeen) {
			ev.LastSeen = &at
			newest = r
		}
	}
	ev.ClientCount = int64(len(clients))
	ev.BlockedBy, ev.listID = reason(newest)
	return ev, nil
}

// reason describes the current filter decision of the newest record as "<source> · <list name> · <category>",
// with the list id still to be resolved to a name.
func reason(r querylog.Record) (string, string) {
	if r.Source == "" || r.Filter == "" || r.Filter == "none" {
		return "", ""
	}
	parts := []string{r.Source}
	if r.Category != "" {
		parts = append(parts, r.Category)
	}
	return strings.Join(parts, " · "), r.ListID
}

// resolveListNames inserts the filter list name of each reason that names a list.
func resolveListNames(ctx context.Context, q store.PolicyQuerier, evs []evidence) error {
	var ids []string
	for _, e := range evs {
		if e.listID != "" && !slices.Contains(ids, e.listID) {
			ids = append(ids, e.listID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `select id::text, name from filter_lists where id::text = any($1)`, ids)
	if err != nil {
		return store.MapError(err)
	}
	names := map[string]string{}
	var id, name string
	if _, err := pgx.ForEachRow(rows, []any{&id, &name}, func() error {
		names[id] = name
		return nil
	}); err != nil {
		return store.MapError(err)
	}
	for i := range evs {
		if n := names[evs[i].listID]; n != "" {
			source, rest, _ := strings.Cut(evs[i].BlockedBy, " · ")
			parts := []string{source, n}
			if rest != "" {
				parts = append(parts, rest)
			}
			evs[i].BlockedBy = strings.Join(parts, " · ")
		}
	}
	return nil
}

// cache stores a verdict for verdictTTL.
func cache(ctx context.Context, st *store.Store, v Verdict, now time.Time) error {
	_, err := st.Pool.Exec(ctx, `insert into ai_domain_verdicts(name, is_threat, categories, confidence, reasoning,
			checked_at, expires_at) values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (name) do update set is_threat = excluded.is_threat, categories = excluded.categories,
			confidence = excluded.confidence, reasoning = excluded.reasoning, checked_at = excluded.checked_at,
			expires_at = excluded.expires_at`,
		v.Name, v.IsThreat, v.Categories, v.Confidence, v.Reasoning, now, now.Add(verdictTTL))
	return store.MapError(err)
}

// validate checks an answer against the batch; its messages are fed back to the model.
func validate(o *output, batch []evidence) error {
	var errs []error
	seen := map[string]bool{}
	for i, a := range o.Verdicts {
		at := fmt.Sprintf("verdicts[%d]", i)
		name := Normalize(a.Name)
		switch {
		case !slices.ContainsFunc(batch, func(e evidence) bool { return e.Name == name }):
			errs = append(errs, fmt.Errorf("%s: %q is not one of the names in the data block", at, a.Name))
			continue
		case seen[name]:
			errs = append(errs, fmt.Errorf("%s: %q is judged twice", at, a.Name))
			continue
		}
		seen[name] = true
		for _, c := range a.Categories {
			if !slices.Contains(Categories, c) {
				errs = append(errs, fmt.Errorf("%s: unknown category %q; known: %s", at, c, categoryList))
			}
		}
		if a.Confidence < 0 || a.Confidence > 1 {
			errs = append(errs, fmt.Errorf("%s: confidence must be between 0 and 1", at))
		}
		if utf8.RuneCountInString(a.Reasoning) > maxReasoning {
			errs = append(errs, fmt.Errorf("%s: reasoning longer than %d characters", at, maxReasoning))
		}
	}
	for _, e := range batch {
		if !seen[e.Name] {
			errs = append(errs, fmt.Errorf("no verdict for %q", e.Name))
		}
	}
	return errors.Join(errs...)
}

// longestLabel is the longest label of name, whose randomness says most about a generated name.
func longestLabel(name string) string {
	longest := ""
	for _, l := range strings.Split(name, ".") {
		if len(l) > len(longest) {
			longest = l
		}
	}
	return longest
}

// entropy is the Shannon entropy of s in bits per character, rounded to two decimals.
func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	h := 0.0
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return math.Round(h*100) / 100
}
