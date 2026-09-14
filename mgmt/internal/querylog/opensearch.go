package querylog

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/piwi3910/nexora/mgmt/internal/config"
)

const openSearchTimeout = 5 * time.Second

// OpenSearch searches the query-log documents an OpenTelemetry Collector's `opensearch` exporter
// writes: the OTLP log attributes under `attributes.*` and the record time in `@timestamp`.
type OpenSearch struct {
	client *opensearchapi.Client
	index  string
}

// NewOpenSearch returns an adapter for cfg; the password, when configured, is read from its file.
func NewOpenSearch(cfg config.OpenSearchConfig) (*OpenSearch, error) {
	var password string
	if cfg.PasswordFile != "" {
		raw, err := os.ReadFile(cfg.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("NEXORA_OPENSEARCH_PASSWORD_FILE: %w", err)
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}
	discover := false
	client, err := opensearchapi.NewClient(opensearchapi.Config{Client: opensearch.Config{
		Addresses:             []string{cfg.URL},
		Username:              cfg.Username,
		Password:              password,
		DisableRetry:          true, // the API answers 503 promptly instead of stacking retries
		HealthCheckMaxRetries: -1,
		DiscoverNodesOnStart:  &discover,
		RequestTimeout:        openSearchTimeout,
	}})
	if err != nil {
		return nil, fmt.Errorf("opensearch client: %w", err)
	}
	index := cfg.Index
	if index == "" {
		index = "nexora-querylog-*"
	}
	return &OpenSearch{client: client, index: index}, nil
}

// Name implements Backend.
func (*OpenSearch) Name() string { return "opensearch" }

type osSource struct {
	Timestamp  time.Time `json:"@timestamp"`
	Attributes struct {
		Client string `json:"client.address"`
		Name   string `json:"dns.question.name"`
		QType  string `json:"dns.question.type"`
		RCode  string `json:"dns.response.code"`
		Cache  string `json:"nexora.cache"`
		Filter string `json:"nexora.filter"` // indices written before nexora-querylog-v2
		// FilterResult is nexora.filter as renamed by the collector's transform/querylog.
		FilterResult string `json:"nexora.filter.result"`
		ListID       string `json:"nexora.filter.list_id"`
		Category     string `json:"nexora.filter.category"`
		Upstream     string `json:"nexora.upstream"`
		Transport    string `json:"nexora.transport"`
		EngineID     string `json:"nexora.engine.id"`
		DurationUS   int64  `json:"nexora.duration_us"`
	} `json:"attributes"`
}

// Search implements Backend. Exact filters use the `.keyword` sub-fields of OpenSearch's default
// dynamic mapping (the analysed text fields lowercase values such as NOERROR); the name filter is
// a phrase match. Transport failures and HTTP 5xx answers are ErrBackendUnavailable.
func (o *OpenSearch) Search(ctx context.Context, q Query) (Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)

	filters := []map[string]any{}
	if !q.From.IsZero() || !q.To.IsZero() {
		r := map[string]any{}
		if !q.From.IsZero() {
			r["gte"] = q.From.UTC().Format(time.RFC3339Nano)
		}
		if !q.To.IsZero() {
			r["lte"] = q.To.UTC().Format(time.RFC3339Nano)
		}
		filters = append(filters, map[string]any{"range": map[string]any{"@timestamp": r}})
	}
	if q.Name != "" {
		filters = append(filters, map[string]any{"match_phrase": map[string]any{"attributes.dns.question.name": q.Name}})
	}
	for field, v := range map[string]string{
		"client.address": q.Client, "dns.question.type": q.QType, "dns.response.code": q.RCode,
		"nexora.cache": q.Cache, "nexora.filter.category": q.Category,
	} {
		if v != "" {
			filters = append(filters, map[string]any{"term": map[string]any{"attributes." + field + ".keyword": v}})
		}
	}
	if q.Filter != "" {
		// nexora-querylog-v2 indices carry nexora.filter.result; older ones nexora.filter.
		filters = append(filters, map[string]any{"bool": map[string]any{
			"should": []map[string]any{
				{"term": map[string]any{"attributes.nexora.filter.keyword": q.Filter}},
				{"term": map[string]any{"attributes.nexora.filter.result.keyword": q.Filter}},
			},
			"minimum_should_match": 1,
		}})
	}
	body := map[string]any{
		"size":             limit + 1,
		"track_total_hits": false,
		"query":            map[string]any{"bool": map[string]any{"filter": filters}},
		// _id breaks ties between records sharing a millisecond, so search_after skips none.
		"sort": []map[string]any{{"@timestamp": map[string]any{"order": "desc"}}, {"_id": map[string]any{"order": "asc"}}},
	}
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		var after []any
		if err != nil || json.Unmarshal(raw, &after) != nil || len(after) == 0 || len(after) > 2 {
			return Page{}, ErrInvalidCursor
		}
		if len(after) == 1 { // a cursor from an instance without the _id tiebreaker
			after = append(after, "")
		}
		body["search_after"] = after
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, openSearchTimeout)
	defer cancel()
	resp, err := o.client.Search(ctx, &opensearchapi.SearchReq{Indices: []string{o.index}, Body: bytes.NewReader(raw)})
	if err != nil {
		if resp == nil || resp.Inspect().Response == nil || resp.Inspect().Response.StatusCode >= 500 {
			return Page{}, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
		}
		return Page{}, fmt.Errorf("opensearch search: %w", err)
	}
	var page Page
	for i, hit := range resp.Hits.Hits {
		if i == limit {
			cursor, err := json.Marshal(resp.Hits.Hits[i-1].Sort)
			if err != nil {
				return Page{}, err
			}
			page.NextCursor = base64.RawURLEncoding.EncodeToString(cursor)
			break
		}
		var src osSource
		if err := json.Unmarshal(hit.Source, &src); err != nil {
			return Page{}, fmt.Errorf("opensearch document %s: %w", hit.ID, err)
		}
		a := src.Attributes
		filter := a.Filter
		if filter == "" {
			filter = a.FilterResult
		}
		page.Records = append(page.Records, Record{
			Time: src.Timestamp, Client: a.Client, Name: a.Name, QType: a.QType, RCode: a.RCode, Cache: a.Cache,
			Filter: filter, Upstream: a.Upstream, Transport: a.Transport, EngineID: a.EngineID,
			ListID: a.ListID, Category: a.Category, DurationUS: a.DurationUS,
		})
	}
	return page, nil
}
