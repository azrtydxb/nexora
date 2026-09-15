package querylog_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

func TestOpenSearchQueriesAttributesAndReportsUnavailable(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if !strings.HasSuffix(r.URL.Path, "/_search") {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":{"hits":[{"_source":{"@timestamp":"2026-09-13T10:00:00Z","attributes":{"client.address":"10.0.0.9","dns.question.name":"q.example.","dns.question.type":"A","dns.response.code":"NOERROR","nexora.cache":"hit","nexora.filter":"none","nexora.upstream":"","nexora.transport":"udp","nexora.engine.id":"e1","nexora.duration_us":77}},"sort":[1]}]}}`)
	}))
	defer srv.Close()
	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.Search(context.Background(), querylog.Query{Name: "q.example", Limit: 5})
	if err != nil || len(page.Records) != 1 || page.Records[0].Cache != "hit" || page.Records[0].DurationUS != 77 {
		t.Fatalf("search: %+v %v", page, err)
	}
	if !strings.Contains(body, "attributes.dns.question.name") {
		t.Fatalf("query does not use attributes.*: %s", body)
	}
	srv.Close()
	if _, err := os.Search(context.Background(), querylog.Query{Limit: 5}); !errors.Is(err, querylog.ErrBackendUnavailable) {
		t.Fatalf("down backend -> %v", err)
	}
}

func TestOpenSearchCategoryAndFilterGenerations(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":{"hits":[{"_source":{"@timestamp":"2026-09-14T10:00:00Z","attributes":{"client.address":"10.0.0.9","dns.question.name":"casino.example.","dns.question.type":"A","dns.response.code":"NOERROR","nexora.cache":"none","nexora.filter.result":"blocked","nexora.filter.list_id":"l1","nexora.filter.category":"gambling","nexora.transport":"udp","nexora.engine.id":"e1","nexora.duration_us":12}},"sort":[1]}]}}`)
	}))
	defer srv.Close()
	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.Search(context.Background(), querylog.Query{Categories: []string{"gambling"}, Filters: []string{"blocked"}, Limit: 5})
	if err != nil || len(page.Records) != 1 || page.Records[0].Filter != "blocked" || page.Records[0].Category != "gambling" || page.Records[0].ListID != "l1" {
		t.Fatalf("v2 document: %+v %v", page, err)
	}
	for _, want := range []string{`"attributes.nexora.filter.category.keyword":["gambling"]`, `"attributes.nexora.filter.result.keyword":["blocked"]`, `"attributes.nexora.filter.keyword":["blocked"]`} {
		if !strings.Contains(body, want) {
			t.Errorf("query lacks %s: %s", want, body)
		}
	}
}

func TestOpenSearchNameWildcardEscaped(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":{"hits":[]}}`)
	}))
	defer srv.Close()
	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Search(context.Background(), querylog.Query{Name: "A*b?c\\.", Limit: 5,
		QTypes: []string{"A", "AAAA"}, Sources: []string{"allowlist"}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"wildcard":{"attributes.dns.question.name.keyword":{"case_insensitive":true,"value":"*A\\*b\\?c\\\\*"}}`,
		`"terms":{"attributes.dns.question.type.keyword":["A","AAAA"]}`,
		`"terms":{"attributes.nexora.filter.source.keyword":["allowlist"]}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "match_phrase") {
		t.Fatalf("name still uses match_phrase: %s", body)
	}
}

func TestOpenSearchSortHasUniqueTiebreaker(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		hit := func(id string) string {
			return `{"_id":"` + id + `","_source":{"@timestamp":"2026-09-14T10:00:00.123Z","attributes":{"dns.question.name":"` + id + `.example."}},"sort":[1789380000123,"` + id + `"]}`
		}
		fmt.Fprintf(w, `{"hits":{"hits":[%s,%s]}}`, hit("a"), hit("b"))
	}))
	defer srv.Close()
	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	page, err := os.Search(ctx, querylog.Query{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	if !strings.Contains(bodies[0], `"sort":[{"@timestamp":{"order":"desc"}},{"_id":{"order":"asc"}}]`) {
		t.Fatalf("sort lacks the _id tiebreaker: %s", bodies[0])
	}
	raw, _ := base64.RawURLEncoding.DecodeString(page.NextCursor)
	var after []any
	if err := json.Unmarshal(raw, &after); err != nil || len(after) != 2 || after[1] != "a" {
		t.Fatalf("cursor = %s", raw)
	}
	if _, err := os.Search(ctx, querylog.Query{Limit: 1, Cursor: page.NextCursor}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies[1], `"search_after":[1789380000123,"a"]`) {
		t.Fatalf("second request: %s", bodies[1])
	}
	old := base64.RawURLEncoding.EncodeToString([]byte(`[1789380000123]`))
	if _, err := os.Search(ctx, querylog.Query{Limit: 1, Cursor: old}); err != nil {
		t.Fatalf("a cursor from an older instance: %v", err)
	}
	if !strings.Contains(bodies[2], `"search_after":[1789380000123,""]`) {
		t.Fatalf("upgraded cursor: %s", bodies[2])
	}
}
