package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/e2e/harness"
)

// TestOpenSearchPagesRecordsSharingAMillisecond indexes three query-log documents with one
// @timestamp into a private index and pages through them one record at a time via the API.
func TestOpenSearchPagesRecordsSharingAMillisecond(t *testing.T) {
	osURL := harness.OpenSearchURL(t)
	index := "nexora-querylog-paging-" + strings.ToLower(strings.TrimSuffix(harness.UniqueName("t"), "."))
	for i := range 3 {
		doc := fmt.Sprintf(`{"@timestamp":"2026-09-14T10:00:00.123Z","attributes":{"client.address":"10.0.0.9","dns.question.name":"r%d.paging.test.","dns.question.type":"A","dns.response.code":"NOERROR","nexora.cache":"miss","nexora.filter":"none","nexora.transport":"udp","nexora.engine.id":"e","nexora.duration_us":1}}`, i)
		resp, err := http.Post(osURL+"/"+index+"/_doc?refresh=true", "application/json", strings.NewReader(doc))
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("index document: %v %v", resp, err)
		}
		resp.Body.Close()
	}
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, osURL+"/"+index, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	})
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{QueryLogBackend: "opensearch", OpenSearchURL: osURL,
		ExtraEnv: []string{"NEXORA_OPENSEARCH_INDEX=" + index}})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	seen := map[string]bool{}
	cursor := ""
	for range 5 {
		var page struct {
			Records []struct {
				Name string `json:"name"`
			} `json:"records"`
			NextCursor string `json:"next_cursor"`
		}
		api.Must("GET", "/query-log?limit=1&name=paging.test&cursor="+url.QueryEscape(cursor), nil, &page, 200)
		for _, r := range page.Records {
			seen[r.Name] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("paged records %v, want all 3 sharing one millisecond", seen)
	}
}
