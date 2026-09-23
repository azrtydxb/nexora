package querylog_test

import (
	"context"
	"slices"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

func TestTopFullPredicates(t *testing.T) {
	b := querylog.NewBuiltin(100)
	req := request("selected.test.", "other.test.", "other.test.", "other.test.")
	attrs := req.ResourceLogs[0].ScopeLogs[0].LogRecords
	attrs[0].Attributes = append(attrs[0].Attributes, str("client.address", "10.0.0.2"), str("dns.question.type", "AAAA"), str("dns.response.code", "NXDOMAIN"), str("nexora.cache", "hit"), str("nexora.filter", "blocked"), str("nexora.filter.category", "ads"), str("nexora.filter.source", "category"), str("nexora.filter.list_id", "list"), str("nexora.policy.group", "group"))
	b.Ingest("selected-engine", req)
	b.Ingest("other-engine", request("other.test.", "other.test."))
	cases := map[string]querylog.Query{
		"client": {Client: "10.0.0.2"}, "name": {Name: "SELECTED.TEST."}, "type": {QTypes: []string{"AAAA", "TXT"}}, "rcode": {RCodes: []string{"NXDOMAIN"}}, "cache": {Caches: []string{"hit"}}, "result": {Filters: []string{"blocked"}}, "category": {Categories: []string{"ads"}}, "source": {Sources: []string{"category"}}, "list": {ListIDs: []string{"list"}}, "group": {PolicyGroups: []string{"group"}},
		"and": {Client: "10.0.0.2", EngineIDs: []string{"other-engine"}}, "engine": {EngineIDs: []string{"selected-engine"}}, "global": {PolicyGroups: []string{"global"}},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			page, err := b.Search(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int64{}
			for _, r := range page.Records {
				counts[r.Name]++
			}
			want := []querylog.TopEntry{}
			for k, n := range counts {
				want = append(want, querylog.TopEntry{Key: k, Count: n})
			}
			slices.SortFunc(want, func(a, b querylog.TopEntry) int {
				if a.Count > b.Count {
					return -1
				}
				if a.Count < b.Count {
					return 1
				}
				if a.Key < b.Key {
					return -1
				}
				if a.Key > b.Key {
					return 1
				}
				return 0
			})
			got, err := b.Top(context.Background(), q.TopQuery(querylog.TopName, 100))
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("Top=%v %v; matching search counts=%v", got, err, want)
			}
		})
	}
}
