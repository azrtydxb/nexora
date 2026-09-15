package api

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai/threat"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// globalAllowlistID and groupAllowPrefix are the list ids the engine reports for the global
// allowlist and for a policy group's allowlist (`group-allow:<sha256>`).
const (
	globalAllowlistID = "allowlist"
	groupAllowPrefix  = "group-allow:"
)

// resolveRecordNames maps records to the API shape and names their filter list, policy group, RPZ
// zone and rewrite answer. Each kind of lookup runs at most once for the page. With withThreats, one
// more query labels the page's names with their cached AI verdicts; the model is never called here.
func resolveRecordNames(ctx context.Context, q store.PolicyQuerier, cat *catalog.Catalog, recs []querylog.Record, withThreats bool) ([]QueryLogRecord, error) {
	var listIDs, groupIDs, rpzIDs, rwNames, rwGroups []string
	for _, r := range recs {
		if r.ListID != "" && r.ListID != globalAllowlistID && !strings.HasPrefix(r.ListID, groupAllowPrefix) {
			listIDs = append(listIDs, r.ListID)
		}
		if r.PolicyGroupID != "" {
			groupIDs = append(groupIDs, r.PolicyGroupID)
		}
		if r.RPZZoneID != "" {
			rpzIDs = append(rpzIDs, r.RPZZoneID)
		}
		if r.Source == string(QueryLogRecordSourceRewrite) && r.Rule != "" {
			rwNames = append(rwNames, r.Rule)
			rwGroups = append(rwGroups, r.PolicyGroupID)
		}
	}
	lists, err := namesByID(ctx, q, "select id::text, name from filter_lists where id::text = any($1)", listIDs)
	if err != nil {
		return nil, err
	}
	groups, err := namesByID(ctx, q, "select id::text, name from policy_groups where id::text = any($1)", groupIDs)
	if err != nil {
		return nil, err
	}
	rpz, err := namesByID(ctx, q, "select id::text, name from rpz_zones where id::text = any($1)", rpzIDs)
	if err != nil {
		return nil, err
	}
	answers, err := rewriteAnswers(ctx, q, rwNames, rwGroups)
	if err != nil {
		return nil, err
	}
	verdicts := map[string]threat.Verdict{}
	if withThreats && len(recs) > 0 {
		names := make([]string, 0, len(recs))
		for _, r := range recs {
			names = append(names, r.Name)
		}
		if verdicts, err = threat.CachedVerdicts(ctx, q, names, time.Now()); err != nil {
			return nil, err
		}
	}

	out := make([]QueryLogRecord, len(recs))
	for i, r := range recs {
		groupName := groups[r.PolicyGroupID]
		listName := lists[r.ListID]
		switch {
		case r.ListID == globalAllowlistID:
			listName = "Global allowlist"
		case strings.HasPrefix(r.ListID, groupAllowPrefix) && groupName != "":
			listName = groupName + " allowlist"
		case strings.HasPrefix(listName, "catalog:"):
			// catalog:<category>:<source> names the catalog source.
			if parts := strings.SplitN(listName, ":", 3); len(parts) == 3 {
				if src, ok := cat.Source(parts[1], parts[2]); ok {
					listName = src.Name
				}
			}
		}
		out[i] = QueryLogRecord{Time: r.Time, Client: r.Client, Name: r.Name, Qtype: r.QType, Rcode: r.RCode,
			Cache: QueryLogRecordCache(r.Cache), Filter: QueryLogRecordFilter(r.Filter), Upstream: r.Upstream,
			Transport: r.Transport, EngineId: r.EngineID, DurationUs: r.DurationUS, ListId: r.ListID, Category: r.Category,
			ListName: listName, Source: QueryLogRecordSource(r.Source), Rule: r.Rule,
			PolicyGroupId: r.PolicyGroupID, PolicyGroupName: groupName, RpzZoneId: r.RPZZoneID, RpzZoneName: rpz[r.RPZZoneID],
			RpzAction: r.RPZAction, UpstreamsRaced: int(r.UpstreamsRaced), Threat: nil}
		if v, ok := verdicts[threat.Normalize(r.Name)]; ok {
			out[i].Threat = &struct {
				Categories []string  `json:"categories"`
				CheckedAt  time.Time `json:"checked_at"`
				Confidence float32   `json:"confidence"`
				IsThreat   bool      `json:"is_threat"`
			}{Categories: v.Categories, CheckedAt: v.CheckedAt, Confidence: float32(v.Confidence), IsThreat: v.IsThreat}
		}
		if r.Source == string(QueryLogRecordSourceRewrite) {
			out[i].RewriteAnswer = answers[rewriteKey{r.Rule, r.PolicyGroupID}]
		}
		// An ACL refusal names the access list that refused it (recursion or authoritative).
		if r.Source == "acl" && r.Rule == "" {
			out[i].Rule = r.ACLRefused
		}
	}
	return out, nil
}

// namesByID runs sql (selecting id and name for the ids in $1) and returns name by id; no query
// runs without ids.
func namesByID(ctx context.Context, q store.PolicyQuerier, sql string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, sql, ids)
	if err != nil {
		return nil, store.MapError(err)
	}
	var id, name string
	if _, err := pgx.ForEachRow(rows, []any{&id, &name}, func() error {
		out[id] = name
		return nil
	}); err != nil {
		return nil, store.MapError(err)
	}
	return out, nil
}

type rewriteKey struct{ name, group string }

// rewriteAnswers returns "<type> <value>" of the first rewrite (by type, value) for each rule name
// and policy group; a group client's rewrite of that name wins over the global one it may also match.
func rewriteAnswers(ctx context.Context, q store.PolicyQuerier, names, groups []string) (map[rewriteKey]string, error) {
	out := map[rewriteKey]string{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `select distinct on (k.name, k.grp) k.name, k.grp, w.type || ' ' || w.value
		from unnest($1::text[], $2::text[]) as k(name, grp)
		join rewrites w on w.name = k.name and coalesce(w.group_id::text, '') in (k.grp, '')
		order by k.name, k.grp, w.group_id is null, w.type, w.value`, names, groups)
	if err != nil {
		return nil, store.MapError(err)
	}
	var k rewriteKey
	var answer string
	if _, err := pgx.ForEachRow(rows, []any{&k.name, &k.group, &answer}, func() error {
		out[k] = answer
		return nil
	}); err != nil {
		return nil, store.MapError(err)
	}
	return out, nil
}
