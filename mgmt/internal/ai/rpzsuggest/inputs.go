// Package rpzsuggest is the RPZ rule suggestion agent (M11 S-12): code selects the candidate names from
// 24 h of query log, the open findings and the cached threat verdicts, the model turns them into RPZ
// rules, and every rule becomes its own suggest-only proposal. Nothing is written into the
// ai-suggested.rpz zone until an operator applies a proposal.
package rpzsuggest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/anomaly"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	// Window is the traffic the inputs are collected over.
	Window = 24 * time.Hour
	// MaxInputs bounds the names one run offers the model.
	MaxInputs = 200
	// maxRecords bounds the query log records one run reads.
	maxRecords = 50000 // debt: newest 50,000 records per run; revisit when a backend offers server-side aggregation
	pageSize   = 1000
	// familyMinNames and familyMinEntropy define a suspicious domain family.
	familyMinNames   = 5
	familyMinEntropy = 3.5
	// threatMinConfidence is the cached verdict confidence a threat input needs.
	threatMinConfidence = 0.7
	// lookalikeMaxDistance is the largest edit distance from a hosted zone label that still reads as typo-squatting.
	lookalikeMaxDistance = 2
)

// Input is one candidate name offered to the model, with the traffic and the reason it was selected.
type Input struct {
	Kind     string `json:"kind"` // candidate|threat|family|lookalike
	Name     string `json:"name"`
	Queries  int64  `json:"queries"`
	Clients  int64  `json:"clients"`
	Evidence string `json:"evidence"`
}

// kindRank orders the inputs by how strong the evidence behind them is.
var kindRank = map[string]int{"threat": 0, "candidate": 1, "lookalike": 2, "family": 3}

// DamerauLevenshtein is the optimal string alignment distance between a and b: insertions, deletions,
// substitutions and transpositions of adjacent characters each cost 1.
func DamerauLevenshtein(a, b string) int {
	x, y := []rune(a), []rune(b)
	prev2 := make([]int, len(y)+1)
	prev := make([]int, len(y)+1)
	cur := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(y)]
}

// nameStat is the 24 h traffic of one queried name.
type nameStat struct {
	queries int64
	clients map[string]struct{}
	blocked bool
}

func normalName(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// blockedRecord reports whether the filter or an RPZ policy other than passthru stopped the query.
func blockedRecord(r querylog.Record) bool {
	return r.Filter == "blocked" || (r.RPZAction != "" && r.RPZAction != "passthru")
}

// firstLabel is the leftmost label of name, and parentOf the rest.
func firstLabel(name string) string {
	label, _, _ := strings.Cut(name, ".")
	return label
}

func parentOf(name string) string {
	_, parent, ok := strings.Cut(name, ".")
	if !ok {
		return ""
	}
	return parent
}

// Collect returns the candidate names of the last 24 h: names of open findings and cached threat verdicts
// that are still answered, high-entropy domain families, and look-alikes of the hosted zones. Names
// already applied as RPZ rules are left out, and at most MaxInputs inputs are returned.
func Collect(ctx context.Context, st *store.Store, ql querylog.Backend, now time.Time) ([]Input, error) {
	names, err := scan(ctx, ql, now.Add(-Window), now)
	if err != nil {
		return nil, err
	}
	applied, err := appliedRecords(ctx, st)
	if err != nil {
		return nil, err
	}
	var out []Input
	taken := map[string]bool{}
	add := func(kind, name, evidence string, queries, clients int64) {
		if name == "" || applied[name] || taken[name] {
			return
		}
		taken[name] = true
		out = append(out, Input{Kind: kind, Name: name, Queries: queries, Clients: clients, Evidence: evidence})
	}
	live := func(name string) *nameStat {
		s := names[name]
		if s == nil || s.blocked {
			return nil
		}
		return s
	}

	threats, err := threatNames(ctx, st, now)
	if err != nil {
		return nil, err
	}
	for _, t := range threats {
		if s := live(t.name); s != nil {
			add("threat", t.name, t.evidence, s.queries, int64(len(s.clients)))
		}
	}
	candidates, err := findingNames(ctx, st)
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if s := live(c.name); s != nil {
			add("candidate", c.name, c.evidence, s.queries, int64(len(s.clients)))
		}
	}
	zones, err := zoneRegistrables(ctx, st)
	if err != nil {
		return nil, err
	}
	for _, l := range lookalikes(names, zones) {
		add(l.Kind, l.Name, l.Evidence, l.Queries, l.Clients)
	}
	for _, f := range families(names) {
		add(f.Kind, f.Name, f.Evidence, f.Queries, f.Clients)
	}

	slices.SortFunc(out, func(a, b Input) int {
		if a.Kind != b.Kind {
			return kindRank[a.Kind] - kindRank[b.Kind]
		}
		if a.Queries != b.Queries {
			return int(b.Queries - a.Queries)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out[:min(len(out), MaxInputs)], nil
}

// scan reads the query log between from and to and returns the traffic per queried name.
func scan(ctx context.Context, ql querylog.Backend, from, to time.Time) (map[string]*nameStat, error) {
	names := map[string]*nameStat{}
	q := querylog.Query{From: from, To: to, Limit: pageSize}
	read := 0
	for read < maxRecords {
		page, err := ql.Search(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Records[:min(len(page.Records), maxRecords-read)] {
			name := normalName(r.Name)
			if name == "" {
				continue
			}
			s := names[name]
			if s == nil {
				s = &nameStat{clients: map[string]struct{}{}}
				names[name] = s
			}
			s.queries++
			s.clients[r.Client] = struct{}{}
			if blockedRecord(r) {
				s.blocked = true
			}
		}
		read += len(page.Records)
		if page.NextCursor == "" || len(page.Records) == 0 {
			break
		}
		q.Cursor = page.NextCursor
	}
	return names, nil
}

func appliedRecords(ctx context.Context, st *store.Store) (map[string]bool, error) {
	rows, err := st.Pool.Query(ctx, "select record from ai_rpz_rules")
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, store.MapError(err)
		}
		out[normalName(r)] = true
	}
	return out, store.MapError(rows.Err())
}

type named struct{ name, evidence string }

// threatNames are the unexpired cached verdicts that call a name a threat with enough confidence.
func threatNames(ctx context.Context, st *store.Store, now time.Time) ([]named, error) {
	rows, err := st.Pool.Query(ctx, `select name, categories, confidence, reasoning from ai_domain_verdicts
		where is_threat and confidence >= $1 and expires_at > $2 order by name`, threatMinConfidence, now)
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	var out []named
	for rows.Next() {
		var name, reasoning string
		var categories []string
		var confidence float64
		if err := rows.Scan(&name, &categories, &confidence, &reasoning); err != nil {
			return nil, store.MapError(err)
		}
		evidence := fmt.Sprintf("threat verdict %s (confidence %.2f)", strings.Join(categories, ", "), confidence)
		if reasoning != "" {
			evidence += ": " + reasoning
		}
		out = append(out, named{normalName(name), evidence})
	}
	return out, store.MapError(rows.Err())
}

// findingNames are the sample domains of the open findings.
func findingNames(ctx context.Context, st *store.Store) ([]named, error) {
	rows, err := st.Pool.Query(ctx, "select type, title, detail from ai_findings where status = 'open' order by last_seen desc")
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	var out []named
	seen := map[string]bool{}
	for rows.Next() {
		var kind, title string
		var detail json.RawMessage
		if err := rows.Scan(&kind, &title, &detail); err != nil {
			return nil, store.MapError(err)
		}
		var d struct {
			SampleDomains []string `json:"sample_domains"`
		}
		if err := json.Unmarshal(detail, &d); err != nil {
			continue
		}
		for _, name := range d.SampleDomains {
			if name = normalName(name); name != "" && !seen[name] {
				seen[name] = true
				out = append(out, named{name, fmt.Sprintf("sample domain of the open %s finding %q", kind, title)})
			}
		}
	}
	return out, store.MapError(rows.Err())
}

// zoneRegistrables is the registrable name of every hosted zone.
func zoneRegistrables(ctx context.Context, st *store.Store) ([]string, error) {
	rows, err := st.Pool.Query(ctx, "select name from zones")
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, store.MapError(err)
		}
		if reg := anomaly.RegistrableParent(normalName(name)); reg != "" && !slices.Contains(out, reg) {
			out = append(out, reg)
		}
	}
	return out, store.MapError(rows.Err())
}

// lookalikes are the queried registrable names whose leftmost label is 1 to 2 edits away from the leftmost
// label of a hosted zone's registrable name.
func lookalikes(names map[string]*nameStat, zones []string) []Input {
	var out []Input
	for name, s := range names {
		if s.blocked || anomaly.RegistrableParent(name) != name {
			continue
		}
		for _, z := range zones {
			d := DamerauLevenshtein(firstLabel(name), firstLabel(z))
			if z == name || d < 1 || d > lookalikeMaxDistance {
				continue
			}
			out = append(out, Input{Kind: "lookalike", Name: name, Queries: s.queries, Clients: int64(len(s.clients)),
				Evidence: fmt.Sprintf("%d edits from the hosted zone %s", d, z)})
			break
		}
	}
	slices.SortFunc(out, func(a, b Input) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// families are the parents with at least familyMinNames distinct queried children whose leftmost label
// looks random; the suggested name is the wildcard *.<parent>.
func families(names map[string]*nameStat) []Input {
	type family struct {
		children int64
		queries  int64
		clients  map[string]struct{}
	}
	byParent := map[string]*family{}
	for name, s := range names {
		if s.blocked || anomaly.Entropy(firstLabel(name)) < familyMinEntropy {
			continue
		}
		parent := parentOf(name)
		if parent == "" || strings.Count(parent, ".") < 1 {
			continue
		}
		f := byParent[parent]
		if f == nil {
			f = &family{clients: map[string]struct{}{}}
			byParent[parent] = f
		}
		f.children++
		f.queries += s.queries
		for c := range s.clients {
			f.clients[c] = struct{}{}
		}
	}
	var out []Input
	for parent, f := range byParent {
		if f.children < familyMinNames {
			continue
		}
		out = append(out, Input{Kind: "family", Name: "*." + parent, Queries: f.queries, Clients: int64(len(f.clients)),
			Evidence: fmt.Sprintf("%d distinct random-looking names under %s", f.children, parent)})
	}
	slices.SortFunc(out, func(a, b Input) int { return strings.Compare(a.Name, b.Name) })
	return out
}
