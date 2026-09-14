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
	Name     string `json:"name"`
	Filter   string `json:"filter"`
	ListID   string `json:"list_id"`
	Category string `json:"category"`
}

func TestQueryLogCategoryAttribution(t *testing.T) {
	for _, backend := range []string{"builtin", "opensearch"} {
		t.Run(backend, func(t *testing.T) {
			s := startCategoryStack(t, "attr-"+backend, func(env *harness.Env) harness.MgmtOptions {
				o := harness.MgmtOptions{QueryLogBackend: backend}
				if backend == "opensearch" {
					col := env.StartOtelcol(harness.OtelcolConfig{OpenSearchURL: harness.OpenSearchURL(t), DebugFile: env.Dir + "/otel.jsonl"})
					o.OpenSearchURL = harness.OpenSearchURL(t)
					o.OTLPEndpoint = "http://" + col.OTLPGRPC
				}
				return o
			})
			casino := strings.TrimSuffix(harness.UniqueName("casino"), ".")
			both := strings.TrimSuffix(harness.UniqueName("both"), ".")
			clean := harness.UniqueName("clean")
			s.lists.SetList(t, "hagezi-gambling", casino+"\n"+both+"\n")
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
		})
	}
}
