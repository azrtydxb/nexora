package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestUT1ArchiveMember(t *testing.T) {
	s := startCategoryStack(t, "ut1-1", nil)
	s.lists.SetList(t, "ut1-gambling", string(harness.UT1Archive(t, map[string]string{
		"blacklists/gambling/domains": "casino.ut1.test\n",
		"blacklists/adult/domains":    "adult.ut1.test\n",
		"blacklists/gambling/urls":    "urls.ut1.test/path\n",
	})))
	s.want(t, "127.0.0.1", "casino.ut1.test.", unblocked)
	s.want(t, "127.0.0.1", "adult.ut1.test.", unblocked)

	if code, reason := s.api.SetFilterCategory("gambling", true, map[string]bool{"hagezi-gambling": false, "blp-gambling": false, "stevenblack-gambling": false}, false); code != http.StatusOK {
		t.Fatalf("enable gambling with only ut1-gambling -> %d %s", code, reason)
	}
	if st := s.api.RefreshSource("gambling", "ut1-gambling"); st.EntryCount != 1 || st.LastError != "" {
		t.Fatalf("ut1-gambling member: %+v", st)
	}
	waitLatestApplied(t, s.api, s.node)
	s.want(t, "127.0.0.1", "casino.ut1.test.", "0.0.0.0")
	s.want(t, "127.0.0.1", "adult.ut1.test.", unblocked) // other members of the archive do not leak in

	// The member disappears from the archive: the error is reported, the last good list stays.
	s.lists.SetList(t, "ut1-gambling", string(harness.UT1Archive(t, map[string]string{"blacklists/adult/domains": "adult.ut1.test\n"})))
	st := s.api.RefreshSource("gambling", "ut1-gambling")
	if st.LastError != "archive member blacklists/gambling/domains not found" || !st.Stale || st.EntryCount != 1 {
		t.Fatalf("missing member: %+v", st)
	}
	s.want(t, "127.0.0.1", "casino.ut1.test.", "0.0.0.0")
	if !s.api.FilterCategory("gambling").Stale {
		t.Fatal("a category with a failing enabled source is not marked stale")
	}

	// A corrupt archive is an error too.
	s.lists.SetList(t, "ut1-gambling", "this is not a gzip archive")
	if st := s.api.RefreshSource("gambling", "ut1-gambling"); !strings.HasPrefix(st.LastError, "read archive: ") || st.EntryCount != 1 {
		t.Fatalf("corrupt archive: %+v", st)
	}
	s.want(t, "127.0.0.1", "casino.ut1.test.", "0.0.0.0")
}

type attributedRecord struct {
	Name            string `json:"name"`
	Filter          string `json:"filter"`
	ListID          string `json:"list_id"`
	Category        string `json:"category"`
	Source          string `json:"source"`
	ListName        string `json:"list_name"`
	Rule            string `json:"rule"`
	PolicyGroupID   string `json:"policy_group_id"`
	PolicyGroupName string `json:"policy_group_name"`
	RPZZoneName     string `json:"rpz_zone_name"`
	RewriteAnswer   string `json:"rewrite_answer"`
}

func TestQueryLogCategoryAttribution(t *testing.T) {
	for _, backend := range []string{"builtin", "opensearch", "clickhouse", "loki"} {
		t.Run(backend, func(t *testing.T) {
			// The decision reason names below are fixed, and the OpenSearch index is shared with earlier
			// runs whose lists no longer exist: those lookups only consider this run's records.
			since := time.Now().UTC().Add(-time.Second).Format(time.RFC3339)
			s := startCategoryStack(t, "attr-"+backend, func(env *harness.Env) harness.MgmtOptions {
				return queryLogMgmtOptions(t, env, backend)
			})
			casino := strings.TrimSuffix(harness.UniqueName("casino"), ".")
			both := strings.TrimSuffix(harness.UniqueName("both"), ".")
			clean := harness.UniqueName("clean")
			// casino.attr.test is the suffix the allowlist checks carve exceptions from.
			s.lists.SetList(t, "hagezi-gambling", casino+"\n"+both+"\ncasino.attr.test\n")
			s.lists.SetList(t, "hagezi-pro", both+"\n")
			if code, reason := s.api.SetFilterCategory("gambling", true, nil, false); code != http.StatusOK {
				t.Fatalf("enable gambling -> %d %s", code, reason)
			}
			if code, reason := s.api.SetFilterCategory("ads-tracking", true, nil, true); code != http.StatusOK {
				t.Fatalf("enable ads-tracking -> %d %s", code, reason)
			}
			s.api.RefreshSource("gambling", "hagezi-gambling")
			s.api.RefreshSource("ads-tracking", "hagezi-pro")
			waitLatestApplied(t, s.api, s.node)
			s.want(t, "127.0.0.1", casino+".", "0.0.0.0")
			s.want(t, "127.0.0.1", both+".", "0.0.0.0")
			s.want(t, "127.0.0.1", clean, unblocked)

			listID := func(category, source string) string {
				for _, src := range s.api.FilterCategory(category).Sources {
					if src.Key == source {
						return src.ListID
					}
				}
				t.Fatalf("source %s missing", source)
				return ""
			}
			search := func(query string) []attributedRecord {
				var page struct {
					Records []attributedRecord `json:"records"`
				}
				s.api.Must(http.MethodGet, "/query-log?"+query, nil, &page, http.StatusOK)
				return page.Records
			}
			find := func(query, name string) (attributedRecord, error) {
				for _, r := range search(query) {
					if strings.TrimSuffix(r.Name, ".") == strings.TrimSuffix(name, ".") {
						return r, nil
					}
				}
				return attributedRecord{}, fmt.Errorf("no record for %s in %s", name, query)
			}
			var r attributedRecord
			harness.Eventually(t, 60*time.Second, func() error {
				var err error
				r, err = find("category=gambling&name="+url.QueryEscape(casino), casino)
				return err
			})
			if r.Filter != "blocked" || r.Category != "gambling" || r.ListID != listID("gambling", "hagezi-gambling") {
				t.Fatalf("casino record: %+v", r)
			}
			// Listed in gambling and ads-tracking: the log names the first category in catalog order.
			harness.Eventually(t, 60*time.Second, func() error {
				var err error
				r, err = find("name="+url.QueryEscape(both), both)
				return err
			})
			if r.Category != "ads-tracking" || r.ListID != listID("ads-tracking", "hagezi-pro") {
				t.Fatalf("record listed in two categories: %+v", r)
			}
			harness.Eventually(t, 60*time.Second, func() error {
				var err error
				r, err = find("name="+url.QueryEscape(strings.TrimSuffix(clean, ".")), clean)
				return err
			})
			if r.Category != "" || r.ListID != "" || r.Filter == "blocked" {
				t.Fatalf("clean record carries attribution: %+v", r)
			}
			if _, err := find("category=ads-tracking&name="+url.QueryEscape(casino), casino); err == nil {
				t.Fatal("the category filter returned a record of another category")
			}

			// Decision reasons (the engine attribution of M6 Task 12). Each check first asserts the
			// DNS answer, then the record the query produced.
			record := func(query, name string) attributedRecord {
				t.Helper()
				var r attributedRecord
				harness.Eventually(t, 60*time.Second, func() error {
					var err error
					r, err = find(query+"from="+url.QueryEscape(since)+"&name="+url.QueryEscape(strings.TrimSuffix(name, ".")), name)
					return err
				})
				return r
			}
			if r := record("category=gambling&", casino); r.Source != "category" || r.ListName != sourceName(t, s.api, "gambling", "hagezi-gambling") {
				t.Fatalf("category reason: %+v", r)
			}

			s.lists.SetList(t, "custom-attr", "custom.attr.test\n")
			var fl struct {
				ID string `json:"id"`
			}
			s.api.Must(http.MethodPost, "/filter-lists", map[string]any{"name": "custom-attr", "kind": "block", "url": s.lists.URL("custom-attr"), "refresh_interval_seconds": 3600, "enabled": true}, &fl, http.StatusCreated)
			s.api.Must(http.MethodPost, "/filter-lists/"+fl.ID+"/refresh", nil, nil, http.StatusOK)
			var allow struct {
				Revision int64 `json:"revision"`
			}
			s.api.Must(http.MethodGet, "/allowlist", nil, &allow, http.StatusOK)
			s.api.Must(http.MethodPut, "/allowlist", map[string]any{"domains": []string{"allow.casino.attr.test"}, "revision": allow.Revision}, nil, http.StatusOK)
			var group struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			s.api.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "attr-group", "cidrs": []string{"127.0.0.2/32"}, "category_keys": []string{"gambling"}, "allowlist": []string{"grp.casino.attr.test"}}, &group, http.StatusCreated)
			s.api.Must(http.MethodPost, "/rewrites", map[string]any{"group_id": nil, "name": "rw.attr.test", "type": "A", "value": "192.0.2.55"}, nil, http.StatusCreated)
			waitLatestApplied(t, s.api, s.node)

			s.want(t, "127.0.0.1", "www.custom.attr.test.", "0.0.0.0")
			if r := record("", "www.custom.attr.test."); r.Source != "blocklist" || r.ListName != "custom-attr" || r.Rule != "custom.attr.test" {
				t.Fatalf("custom list reason: %+v", r)
			}
			s.want(t, "127.0.0.1", "www.casino.attr.test.", "0.0.0.0")
			s.want(t, "127.0.0.1", "allow.casino.attr.test.", unblocked)
			if r := record("", "allow.casino.attr.test."); r.Source != "allowlist" || r.Rule != "allow.casino.attr.test" || r.PolicyGroupName != "" {
				t.Fatalf("global allowlist reason: %+v", r)
			}
			s.want(t, "127.0.0.2", "www.casino.attr.test.", "0.0.0.0")
			s.want(t, "127.0.0.2", "grp.casino.attr.test.", unblocked)
			if r := record("", "grp.casino.attr.test."); r.Source != "allowlist" || r.PolicyGroupName != group.Name || r.PolicyGroupID != group.ID {
				t.Fatalf("group allowlist reason: %+v", r)
			}
			s.want(t, "127.0.0.1", "rw.attr.test.", "192.0.2.55")
			if r := record("", "rw.attr.test."); r.Source != "rewrite" || r.Rule != "rw.attr.test" || r.RewriteAnswer != "A 192.0.2.55" {
				t.Fatalf("rewrite reason: %+v", r)
			}
		})
	}
}

// sourceName is the catalog display name of a category source, as the API lists it.
func sourceName(t *testing.T, api *harness.API, category, source string) string {
	t.Helper()
	var all []struct {
		Key     string `json:"key"`
		Sources []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"sources"`
	}
	api.Must(http.MethodGet, "/filter-categories", nil, &all, http.StatusOK)
	for _, c := range all {
		for _, src := range c.Sources {
			if c.Key == category && src.Key == source {
				return src.Name
			}
		}
	}
	t.Fatalf("source %s/%s missing", category, source)
	return ""
}
