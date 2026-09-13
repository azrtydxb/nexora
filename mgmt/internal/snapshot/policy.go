package snapshot

import (
	"cmp"
	"slices"

	"github.com/google/uuid"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// PolicySection is the policy part of a ConfigSnapshot.
type PolicySection struct {
	Groups              []*controlv1.PolicyGroup
	RewriteSets         []*controlv1.RewriteSet
	GlobalRewriteSetIDs []string
}

var rewriteTypes = map[string]controlv1.RewriteType{
	"A":     controlv1.RewriteType_REWRITE_TYPE_A,
	"AAAA":  controlv1.RewriteType_REWRITE_TYPE_AAAA,
	"CNAME": controlv1.RewriteType_REWRITE_TYPE_CNAME,
}

// BuildPolicySection turns stored groups, rewrites and global safe search into snapshot messages.
// listBlobs holds the current blob of every fetched blocklist, enabled or not; a group's list
// without a blob is skipped. Safe-search sets are emitted once however many scopes use them.
func BuildPolicySection(groups []store.PolicyGroup, listBlobs map[uuid.UUID]*controlv1.BlobRef, rewrites []store.Rewrite, global store.SafeSearch) PolicySection {
	groups = slices.Clone(groups)
	slices.SortFunc(groups, func(a, b store.PolicyGroup) int { return cmp.Compare(a.Name, b.Name) })
	rewrites = slices.Clone(rewrites)
	slices.SortFunc(rewrites, func(a, b store.Rewrite) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Type, b.Type), cmp.Compare(a.Value, b.Value))
	})

	custom := map[string]*controlv1.RewriteSet{}
	var sec PolicySection
	addRule := func(id, label string, r store.Rewrite) {
		set := custom[id]
		if set == nil {
			set = &controlv1.RewriteSet{Id: id, Label: label}
			custom[id] = set
		}
		set.Rules = append(set.Rules, &controlv1.RewriteRule{Name: r.Name, Type: rewriteTypes[r.Type], Value: r.Value, Ttl: uint32(r.TTL)})
	}
	names := map[uuid.UUID]string{}
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	for _, r := range rewrites {
		if r.GroupID == nil {
			addRule("custom:global", "Custom rewrites", r)
		} else if name, ok := names[*r.GroupID]; ok {
			addRule("custom:group:"+r.GroupID.String(), "Custom rewrites: "+name, r)
		}
	}

	var safeOrder []string
	seen := map[string]bool{}
	scopeSets := func(customID string, ss store.SafeSearch) []string {
		ids := []string{}
		if set := custom[customID]; set != nil {
			ids = append(ids, customID)
			sec.RewriteSets = append(sec.RewriteSets, set)
		}
		for _, id := range safeSearchIDs(ss) {
			ids = append(ids, id)
			if !seen[id] {
				seen[id] = true
				safeOrder = append(safeOrder, id)
			}
		}
		return ids
	}

	sec.GlobalRewriteSetIDs = scopeSets("custom:global", global)
	for _, g := range groups {
		pg := &controlv1.PolicyGroup{
			Id:            g.ID.String(),
			Name:          g.Name,
			Allowlist:     slices.Clone(g.Allowlist),
			RewriteSetIds: scopeSets("custom:group:"+g.ID.String(), g.SafeSearch),
		}
		for _, c := range g.CIDRs {
			pg.Cidrs = append(pg.Cidrs, c.String())
		}
		for _, id := range g.FilterListIDs {
			if ref := listBlobs[id]; ref != nil {
				pg.Blocklists = append(pg.Blocklists, ref)
			}
		}
		sec.Groups = append(sec.Groups, pg)
	}
	for _, id := range safeOrder {
		sec.RewriteSets = append(sec.RewriteSets, SafeSearchSet(id))
	}
	return sec
}
