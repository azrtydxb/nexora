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

func TestBuiltinTop(t *testing.T) {
	b := querylog.NewBuiltin(20)
	req := request("a.test.", "a.test.", "b.test.", "blocked.test.", "blocked.test.", "blocked.test.")
	for _, lr := range req.ResourceLogs[0].ScopeLogs[0].LogRecords[3:] {
		lr.Attributes = append(lr.Attributes, str("nexora.filter.result", "blocked"), str("nexora.filter.category", "ads-tracking"))
	}
	b.Ingest("e1", req)
	top := func(q querylog.TopQuery) []querylog.TopEntry {
		q.Limit = 2
		out, err := b.Top(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := top(querylog.TopQuery{Field: "name"}); len(got) != 2 || got[0].Key != "blocked.test." || got[0].Count != 3 || got[1].Key != "a.test." {
		t.Fatalf("top names: %+v", got)
	}
	if got := top(querylog.TopQuery{Field: "name", Filters: []string{"blocked"}}); len(got) != 1 {
		t.Fatalf("top blocked: %+v", got)
	}
	if got := top(querylog.TopQuery{Field: "category"}); len(got) != 1 || got[0].Key != "ads-tracking" {
		t.Fatalf("top categories skip empty keys: %+v", got)
	}
	if got := top(querylog.TopQuery{Field: "client"}); len(got) != 1 || got[0].Key != "10.0.0.1" || got[0].Count != 6 {
		t.Fatalf("top clients: %+v", got)
	}
	var _ querylog.Topper = b
}

func TestOpenSearchTopAggregation(t *testing.T) {
	var body string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			fmt.Fprint(w, `{"error":"boom"}`)
			return
		}
		fmt.Fprint(w, `{"hits":{"hits":[]},"aggregations":{"top":{"buckets":[{"key":"","doc_count":9},{"key":"blocked.test.","doc_count":3}]}}}`)
	}))
	defer srv.Close()
	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
	if err != nil {
		t.Fatal(err)
	}
	// Limit 1 asks OpenSearch for one extra bucket, so the dropped empty key still leaves one entry.
	got, err := os.Top(context.Background(), querylog.TopQuery{Field: "name", Filters: []string{"blocked"}, Limit: 1})
	if err != nil || len(got) != 1 || got[0].Key != "blocked.test." || got[0].Count != 3 {
		t.Fatalf("top: %+v %v", got, err)
	}
	for _, want := range []string{
		`"aggs":{"top":{"terms":{"field":"attributes.dns.question.name.keyword","size":2}}}`,
		`"size":0`,
		`"attributes.nexora.filter.result.keyword":["blocked"]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %s:\n%s", want, body)
		}
	}
	if _, err := os.Top(context.Background(), querylog.TopQuery{Field: "client", Limit: 5}); err != nil || !strings.Contains(body, `"field":"attributes.client.address.keyword"`) {
		t.Fatalf("client field: %v %s", err, body)
	}
	status = http.StatusInternalServerError
	if _, err := os.Top(context.Background(), querylog.TopQuery{Field: "category", Limit: 5}); !errors.Is(err, querylog.ErrBackendUnavailable) {
		t.Fatalf("500 -> %v, want ErrBackendUnavailable", err)
	}
}
