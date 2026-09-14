package querylog_test

import (
	"context"
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
	page, err := os.Search(context.Background(), querylog.Query{Category: "gambling", Filter: "blocked", Limit: 5})
	if err != nil || len(page.Records) != 1 || page.Records[0].Filter != "blocked" || page.Records[0].Category != "gambling" || page.Records[0].ListID != "l1" {
		t.Fatalf("v2 document: %+v %v", page, err)
	}
	for _, want := range []string{`"attributes.nexora.filter.category.keyword":"gambling"`, `"attributes.nexora.filter.result.keyword":"blocked"`, `"attributes.nexora.filter.keyword":"blocked"`} {
		if !strings.Contains(body, want) {
			t.Errorf("query lacks %s: %s", want, body)
		}
	}
}
