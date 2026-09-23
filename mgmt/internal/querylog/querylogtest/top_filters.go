package querylogtest

import (
	"cmp"
	"context"
	"slices"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

// Compare aggregates to independently counted search records, so a shared Top regression cannot
// pass merely because every adapter (including the reference Top) drops the same predicate.
func fullTopPredicates(tb testing.TB, topper querylog.Topper, ref *querylog.Builtin, all querylog.Query) {
	cases := map[string]querylog.Query{
		"client": {Client: "10.0.0.2"}, "domain": {Name: "YOU-" + all.Name}, "type": {QTypes: []string{"AAAA", "MX"}},
		"result": {Filters: []string{"blocked", "allowed"}}, "rcode": {RCodes: []string{"NXDOMAIN"}}, "cache": {Caches: []string{"hit"}},
		"category": {Categories: []string{"gambling"}}, "source": {Sources: []string{"category", "allowlist"}},
		"list": {ListIDs: []string{"l-custom"}}, "group": {PolicyGroups: []string{"g1"}}, "global": {PolicyGroups: []string{"global"}}, "engine": {EngineIDs: []string{"e2"}},
		"combined":           {Client: "10.0.0.2", QTypes: []string{"A"}, RCodes: []string{"NXDOMAIN"}, Caches: []string{"hit"}, EngineIDs: []string{"e1"}, PolicyGroups: []string{"global"}},
		"empty-intersection": {Client: "10.0.0.2", EngineIDs: []string{"e2"}},
	}
	for name, q := range cases {
		sub(tb, "top-filter-"+name, func(tb testing.TB) {
			q.From, q.To = all.From, all.To
			if q.Name == "" {
				q.Name = all.Name
			}
			q.Limit = 1000
			page, err := ref.Search(context.Background(), q)
			if err != nil {
				tb.Fatal(err)
			}
			for _, field := range []querylog.TopField{querylog.TopName, querylog.TopClient, querylog.TopCategory} {
				counts := map[string]int64{}
				for _, r := range page.Records {
					var key string
					switch field {
					case querylog.TopName:
						key = r.Name
					case querylog.TopClient:
						key = r.Client
					case querylog.TopCategory:
						key = r.Category
					}
					if key != "" {
						counts[key]++
					}
				}
				want := []querylog.TopEntry{}
				for key, n := range counts {
					want = append(want, querylog.TopEntry{Key: key, Count: n})
				}
				slices.SortFunc(want, func(a, b querylog.TopEntry) int {
					return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Key, b.Key))
				})
				// Search paging must not truncate the aggregate population.
				q.Limit, q.Cursor = 1, "not-an-aggregate-cursor"
				got, err := topper.Top(context.Background(), q.TopQuery(field, 100))
				if err != nil || !slices.Equal(got, want) {
					tb.Errorf("%s: got %v %v; matching records count to %v", field, got, err, want)
				}
			}
		})
	}
}
