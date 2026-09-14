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
		Source       string `json:"nexora.filter.source"`
		Rule         string `json:"nexora.filter.rule"`
		PolicyGroup  string `json:"nexora.policy.group"`
		RPZZone      string `json:"nexora.rpz_zone"`
		RPZAction    string `json:"nexora.rpz"`
		ACLRefused   string `json:"nexora.acl.refused"`
		Raced        int64  `json:"nexora.upstream_raced"`
	} `json:"attributes"`
}

// Search implements Backend. Exact filters use the `.keyword` sub-fields of OpenSearch's default
// dynamic mapping (the analysed text fields lowercase values such as NOERROR); each multi-value
// filter is one `terms` query and the name filter is a case-insensitive wildcard. Transport
// failures and HTTP 5xx answers are ErrBackendUnavailable.
func (o *OpenSearch) Search(ctx context.Context, q Query) (Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	body := map[string]any{
		"size":             limit + 1,
		"track_total_hits": false,
		"query":            map[string]any{"bool": map[string]any{"filter": filterClauses(q)}},
		// debt: @timestamp has millisecond resolution, so records sharing the millisecond at a
		// page boundary can be skipped by search_after; revisit with a unique tiebreaker field.
		"sort": []map[string]any{{"@timestamp": "desc"}},
	}
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		var after []any
		if err != nil || json.Unmarshal(raw, &after) != nil || len(after) == 0 {
			return Page{}, ErrInvalidCursor
		}
		body["search_after"] = after
	}
	resp, err := o.search(ctx, body)
	if err != nil {
		return Page{}, err
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
		rpzAction := a.RPZAction
		if rpzAction == "none" {
			rpzAction = ""
		}
		page.Records = append(page.Records, Record{
			Time: src.Timestamp, Client: a.Client, Name: a.Name, QType: a.QType, RCode: a.RCode, Cache: a.Cache,
			Filter: filter, Upstream: a.Upstream, Transport: a.Transport, EngineID: a.EngineID,
			ListID: a.ListID, Category: a.Category, DurationUS: a.DurationUS,
			Source: a.Source, Rule: a.Rule, PolicyGroupID: a.PolicyGroup, RPZZoneID: a.RPZZone, RPZAction: rpzAction,
			ACLRefused: a.ACLRefused, UpstreamsRaced: a.Raced,
		})
	}
	return page, nil
}

// topFields maps each top list field to its document attribute.
var topFields = map[TopField]string{TopName: "dns.question.name", TopClient: "client.address", TopCategory: "nexora.filter.category"}

// Top implements Topper with a terms aggregation on the attribute's keyword field under the same
// time and filter clauses as Search. It asks for one bucket more than Limit because records without
// the attribute can aggregate under the empty key, which is dropped.
func (o *OpenSearch) Top(ctx context.Context, q TopQuery) ([]TopEntry, error) {
	field, ok := topFields[q.Field]
	if !ok {
		return nil, fmt.Errorf("top list field %q", q.Field)
	}
	body := map[string]any{
		"size":             0,
		"track_total_hits": false,
		"query":            map[string]any{"bool": map[string]any{"filter": filterClauses(Query{From: q.From, To: q.To, Filters: q.Filters})}},
		"aggs":             map[string]any{"top": map[string]any{"terms": map[string]any{"field": "attributes." + field + ".keyword", "size": q.Limit + 1}}},
	}
	resp, err := o.search(ctx, body)
	if err != nil {
		return nil, err
	}
	var aggs struct {
		Top struct {
			Buckets []struct {
				Key      string `json:"key"`
				DocCount int64  `json:"doc_count"`
			} `json:"buckets"`
		} `json:"top"`
	}
	if len(resp.Aggregations) > 0 {
		if err := json.Unmarshal(resp.Aggregations, &aggs); err != nil {
			return nil, fmt.Errorf("opensearch aggregation: %w", err)
		}
	}
	out := []TopEntry{}
	for _, b := range aggs.Top.Buckets {
		if b.Key != "" && len(out) < q.Limit {
			out = append(out, TopEntry{Key: b.Key, Count: b.DocCount})
		}
	}
	return out, nil
}

// search runs body against the index; transport failures and HTTP 5xx answers are
// ErrBackendUnavailable.
func (o *OpenSearch) search(ctx context.Context, body map[string]any) (*opensearchapi.SearchResp, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, openSearchTimeout)
	defer cancel()
	resp, err := o.client.Search(ctx, &opensearchapi.SearchReq{Indices: []string{o.index}, Body: bytes.NewReader(raw)})
	if err != nil {
		if resp == nil || resp.Inspect().Response == nil || resp.Inspect().Response.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
		}
		return nil, fmt.Errorf("opensearch search: %w", err)
	}
	return resp, nil
}

// filterClauses is the bool filter of q's time range and field filters (limit and cursor aside).
func filterClauses(q Query) []map[string]any {
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
	if name := strings.TrimSuffix(q.Name, "."); name != "" {
		// debt: leading-wildcard on .keyword scans every term of the day's index; revisit with an n-gram sub-field when a daily index passes 50M documents or searches exceed the 5 s timeout on kw
		filters = append(filters, map[string]any{"wildcard": map[string]any{"attributes.dns.question.name.keyword": map[string]any{
			"value": "*" + EscapeWildcard(name) + "*", "case_insensitive": true,
		}}})
	}
	if q.Client != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"attributes.client.address.keyword": q.Client}})
	}
	for _, f := range []struct {
		field  string
		values []string
	}{
		{"dns.question.type", q.QTypes}, {"dns.response.code", q.RCodes}, {"nexora.cache", q.Caches},
		{"nexora.filter.category", q.Categories}, {"nexora.filter.source", q.Sources},
		{"nexora.filter.list_id", q.ListIDs}, {"nexora.engine.id", q.EngineIDs},
	} {
		if len(f.values) > 0 {
			filters = append(filters, terms(f.field, f.values))
		}
	}
	if len(q.Filters) > 0 {
		// nexora-querylog-v2 indices carry nexora.filter.result; older ones nexora.filter.
		filters = append(filters, should(terms("nexora.filter", q.Filters), terms("nexora.filter.result", q.Filters)))
	}
	if len(q.PolicyGroups) > 0 {
		var ids []string
		var alternatives []map[string]any
		for _, g := range q.PolicyGroups {
			if g == GlobalPolicyGroup {
				// Global clients carry no attribute or an empty one.
				alternatives = append(alternatives,
					map[string]any{"bool": map[string]any{"must_not": map[string]any{"exists": map[string]any{"field": "attributes.nexora.policy.group"}}}},
					map[string]any{"term": map[string]any{"attributes.nexora.policy.group.keyword": ""}})
			} else {
				ids = append(ids, g)
			}
		}
		if len(ids) > 0 {
			alternatives = append(alternatives, terms("nexora.policy.group", ids))
		}
		filters = append(filters, should(alternatives...))
	}
	return filters
}

// terms matches documents whose attribute field equals one of values.
func terms(field string, values []string) map[string]any {
	return map[string]any{"terms": map[string]any{"attributes." + field + ".keyword": values}}
}

// should matches documents that match at least one of the queries.
func should(queries ...map[string]any) map[string]any {
	return map[string]any{"bool": map[string]any{"should": queries, "minimum_should_match": 1}}
}
