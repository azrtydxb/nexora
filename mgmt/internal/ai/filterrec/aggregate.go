// Package filterrec is the filter and policy recommendations agent (M11 S-6): code aggregates 24 h of
// query log per policy group and engine group, the model chooses and explains at most 10 recommendations,
// and code turns each into a validated proposal with impact numbers computed from the aggregate.
package filterrec

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	// MaxRecords bounds the records one aggregate reads.
	MaxRecords = 50000 // debt: newest 50,000 records per run; revisit when a backend offers server-side aggregation for these counts
	pageSize   = 1000
	// maxCheckedNames bounds the queried names looked up in disabled categories' list blobs.
	maxCheckedNames = 2000
	topUnblocked    = 50
)

// GroupStats is the 24 h traffic of one policy group ("" = global) on one engine group ("" = unknown).
type GroupStats struct {
	PolicyGroupID        string           `json:"policy_group_id"`
	EngineGroupID        string           `json:"engine_group_id"`
	Total                int64            `json:"total"`
	Blocked              int64            `json:"blocked"`
	BlockedByCategory    map[string]int64 `json:"blocked_by_category"`
	Unblocked            []NameCount      `json:"unblocked"`    // top 50 names neither blocked nor allowed
	SearchHosts          map[string]int64 `json:"search_hosts"` // google, bing, duckduckgo, youtube host families
	SafeSearch           SafeSearchState  `json:"safe_search"`
	DisabledCategoryHits map[string]int64 `json:"disabled_category_hits"` // disabled category key -> not-blocked queries whose name is in its source blobs

	unblocked map[string]*nameAcc // every not-blocked, not-allowed name
	blocked   map[string]int64    // every blocked name
}

// NameCount is one queried name with its query and distinct client counts.
type NameCount struct {
	Name    string `json:"name"`
	Queries int64  `json:"queries"`
	Clients int64  `json:"clients"`
}

// SafeSearchState is a safe-search selection; YouTube is off, moderate or strict.
type SafeSearchState struct {
	Google     bool   `json:"google"`
	Bing       bool   `json:"bing"`
	DuckDuckGo bool   `json:"duckduckgo"`
	YouTube    string `json:"youtube"`
}

type nameAcc struct {
	queries int64
	clients map[string]struct{}
}

func stateOf(s store.SafeSearch) SafeSearchState {
	return SafeSearchState{Google: s.Google, Bing: s.Bing, DuckDuckGo: s.DuckDuckGo, YouTube: s.YouTube}
}

func normalName(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

func atOrUnder(name, parent string) bool {
	return name == parent || strings.HasSuffix(name, "."+parent)
}

// searchFamily names the search or video host family of name, or "".
func searchFamily(name string) string {
	for _, l := range strings.Split(name, ".") {
		switch l {
		case "youtube", "youtube-nocookie":
			return "youtube"
		case "google":
			return "google"
		case "bing":
			return "bing"
		case "duckduckgo":
			return "duckduckgo"
		}
	}
	return ""
}

// blockedRecord reports whether the filter or an RPZ policy other than passthru stopped the query.
func blockedRecord(r querylog.Record) bool {
	return r.Filter == "blocked" || (r.RPZAction != "" && r.RPZAction != "passthru")
}

// groupIndex assigns a record to its policy group: the engine's attribution when present, otherwise the
// longest client CIDR among the groups in scope for the engine's engine group.
type groupIndex struct {
	groups        []store.PolicyGroup
	byID          map[string]*store.PolicyGroup
	engineGroupOf map[string]string
}

func (gi *groupIndex) assign(r querylog.Record) (group *store.PolicyGroup, engineGroup string) {
	engineGroup = gi.engineGroupOf[r.EngineID]
	if g, ok := gi.byID[r.PolicyGroupID]; ok {
		return g, engineGroup
	}
	addr, err := netip.ParseAddr(r.Client)
	if err != nil {
		return nil, engineGroup
	}
	addr = addr.Unmap()
	best := -1
	for i := range gi.groups {
		g := &gi.groups[i]
		if g.EngineGroupID != nil && g.EngineGroupID.String() != engineGroup {
			continue
		}
		for _, p := range g.CIDRs {
			if p.Contains(addr) && p.Bits() > best {
				best, group = p.Bits(), g
			}
		}
	}
	return group, engineGroup
}

// Aggregate reads the query log between from and to (newest first, at most MaxRecords) and returns one
// GroupStats per policy group and engine group with traffic, ordered by policy group then engine group.
func Aggregate(ctx context.Context, st *store.Store, ql querylog.Backend, cat *catalog.Catalog, from, to time.Time) ([]GroupStats, error) {
	groups, err := store.ListPolicyGroups(ctx, st.Pool)
	if err != nil {
		return nil, fmt.Errorf("policy groups: %w", err)
	}
	global, err := store.GetGlobalSafeSearch(ctx, st.Pool)
	if err != nil {
		return nil, fmt.Errorf("global safe search: %w", err)
	}
	var globalAllow []string
	if err := st.Pool.QueryRow(ctx, "select domains from allowlist").Scan(&globalAllow); err != nil {
		return nil, fmt.Errorf("allowlist: %w", store.MapError(err))
	}
	gi := &groupIndex{groups: groups, byID: map[string]*store.PolicyGroup{}, engineGroupOf: map[string]string{}}
	for i := range groups {
		gi.byID[groups[i].ID.String()] = &groups[i]
	}
	rows, err := st.Pool.Query(ctx, "select id::text, engine_group_id::text from engines")
	if err != nil {
		return nil, fmt.Errorf("engines: %w", store.MapError(err))
	}
	for rows.Next() {
		var id, eg string
		if err := rows.Scan(&id, &eg); err != nil {
			rows.Close()
			return nil, store.MapError(err)
		}
		gi.engineGroupOf[id] = eg
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, store.MapError(err)
	}

	byKey := map[[2]string]*GroupStats{}
	q := querylog.Query{From: from, To: to, Limit: pageSize}
	read := 0
	for read < MaxRecords {
		page, err := ql.Search(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Records[:min(len(page.Records), MaxRecords-read)] {
			add(byKey, gi, global.SafeSearch, globalAllow, r)
		}
		read += len(page.Records)
		if page.NextCursor == "" || len(page.Records) == 0 {
			break
		}
		q.Cursor = page.NextCursor
	}

	out := make([]GroupStats, 0, len(byKey))
	for _, s := range byKey {
		s.Unblocked = top(s.unblocked, topUnblocked)
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b GroupStats) int {
		return cmp.Or(strings.Compare(a.PolicyGroupID, b.PolicyGroupID), strings.Compare(a.EngineGroupID, b.EngineGroupID))
	})
	if err := disabledHits(ctx, st, cat, gi, out); err != nil {
		return nil, err
	}
	return out, nil
}

func add(byKey map[[2]string]*GroupStats, gi *groupIndex, global store.SafeSearch, globalAllow []string, r querylog.Record) {
	g, eg := gi.assign(r)
	key := [2]string{"", eg}
	ss := global
	var groupAllow []string
	if g != nil {
		key[0] = g.ID.String()
		ss, groupAllow = g.SafeSearch, g.Allowlist
	}
	s := byKey[key]
	if s == nil {
		s = &GroupStats{PolicyGroupID: key[0], EngineGroupID: eg, BlockedByCategory: map[string]int64{}, SearchHosts: map[string]int64{},
			SafeSearch: stateOf(ss), DisabledCategoryHits: map[string]int64{}, unblocked: map[string]*nameAcc{}, blocked: map[string]int64{}}
		byKey[key] = s
	}
	name := normalName(r.Name)
	s.Total++
	if f := searchFamily(name); f != "" {
		s.SearchHosts[f]++
	}
	switch {
	case blockedRecord(r):
		s.Blocked++
		s.blocked[name]++
		category := r.Category
		if category == "" {
			category = "uncategorized"
		}
		s.BlockedByCategory[category]++
	case r.Filter == "allowed" || allowlisted(name, globalAllow) || allowlisted(name, groupAllow):
	default:
		acc := s.unblocked[name]
		if acc == nil {
			acc = &nameAcc{clients: map[string]struct{}{}}
			s.unblocked[name] = acc
		}
		acc.queries++
		acc.clients[r.Client] = struct{}{}
	}
}

func allowlisted(name string, domains []string) bool {
	return slices.ContainsFunc(domains, func(d string) bool { return atOrUnder(name, normalName(d)) })
}

func top(names map[string]*nameAcc, n int) []NameCount {
	out := make([]NameCount, 0, len(names))
	for name, acc := range names {
		out = append(out, NameCount{Name: name, Queries: acc.queries, Clients: int64(len(acc.clients))})
	}
	slices.SortFunc(out, func(a, b NameCount) int {
		return cmp.Or(cmp.Compare(b.Queries, a.Queries), strings.Compare(a.Name, b.Name))
	})
	return out[:min(len(out), n)]
}

// disabledHits fills DisabledCategoryHits: for every catalog category not in effect for a group (not
// enabled globally and, for a policy group, not among its category keys), the group's not-blocked queries
// whose name or a parent of it is listed in the current blob of one of the category's enabled sources.
// Only the maxCheckedNames most queried names are looked up.
func disabledHits(ctx context.Context, st *store.Store, cat *catalog.Catalog, gi *groupIndex, stats []GroupStats) error {
	cats, err := store.ListFilterCategories(ctx, st.Pool)
	if err != nil {
		return fmt.Errorf("filter categories: %w", err)
	}
	enabled := map[string]bool{}
	var keys []string
	for _, c := range cats {
		enabled[c.Key] = c.Enabled
		keys = append(keys, c.Key)
	}
	if cat != nil {
		keys = keys[:0]
		for _, c := range cat.Categories {
			keys = append(keys, c.Key)
		}
	}
	disabled := func(s GroupStats, key string) bool {
		if enabled[key] {
			return false
		}
		g := gi.byID[s.PolicyGroupID]
		return g == nil || !slices.Contains(g.CategoryKeys, key)
	}
	var wanted []string
	for _, k := range keys {
		for i := range stats {
			if disabled(stats[i], k) {
				stats[i].DisabledCategoryHits[k] = 0
				if !slices.Contains(wanted, k) {
					wanted = append(wanted, k)
				}
			}
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	// The most queried not-blocked names, and every suffix of them, since a listed name blocks its subdomains.
	totals := map[string]*nameAcc{}
	for _, s := range stats {
		for name, acc := range s.unblocked {
			t := totals[name]
			if t == nil {
				t = &nameAcc{}
				totals[name] = t
			}
			t.queries += acc.queries
		}
	}
	covers := map[string][]string{} // listed suffix -> checked names at or under it
	for _, nc := range top(totals, maxCheckedNames) {
		for suffix := nc.Name; suffix != ""; {
			covers[suffix] = append(covers[suffix], nc.Name)
			_, rest, ok := strings.Cut(suffix, ".")
			if !ok {
				break
			}
			suffix = rest
		}
	}

	hits := map[string]map[string]bool{} // category -> checked names listed
	rows, err := st.Pool.Query(ctx, `select f.category_key, b.data from filter_lists f join blobs b on b.sha256 = f.current_blob_sha256
		where f.managed_by_catalog and f.enabled and f.category_key = any($1)`, wanted)
	if err != nil {
		return fmt.Errorf("category lists: %w", store.MapError(err))
	}
	defer rows.Close()
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	defer dec.Close()
	for rows.Next() {
		var key string
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			return store.MapError(err)
		}
		if err := dec.Reset(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("category %s list blob: %w", key, err)
		}
		sc := bufio.NewScanner(dec)
		sc.Buffer(make([]byte, 0, 1024), 1<<16)
		for sc.Scan() {
			for _, name := range covers[sc.Text()] {
				if hits[key] == nil {
					hits[key] = map[string]bool{}
				}
				hits[key][name] = true
			}
		}
		if err := sc.Err(); err != nil {
			return fmt.Errorf("category %s list blob: %w", key, err)
		}
	}
	if err := rows.Err(); err != nil {
		return store.MapError(err)
	}
	for i := range stats {
		for key := range stats[i].DisabledCategoryHits {
			for name := range hits[key] {
				if acc := stats[i].unblocked[name]; acc != nil {
					stats[i].DisabledCategoryHits[key] += acc.queries
				}
			}
		}
	}
	return nil
}
