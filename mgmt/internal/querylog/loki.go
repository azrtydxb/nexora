package querylog

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/config"
)

const (
	lokiSearchTimeout = 5 * time.Second
	lokiTopTimeout    = 15 * time.Second
	// lokiMaxEntries is Loki's default max_entries_limit_per_query.
	lokiMaxEntries = 5000
	// lokiPartitionConcurrency bounds the partition queries of the series-limit fallback.
	lokiPartitionConcurrency = 6
	lokiDefaultSelector      = `{service_name="nexora-engine"}`
	lokiDefaultLookback      = 168 * time.Hour
)

// errLokiSeriesLimit is Loki's HTTP 400 for a metric query over max_query_series.
var errLokiSeriesLimit = errors.New("loki series limit")

// Loki searches the query log in Loki, where the OpenTelemetry Collector's otlphttp exporter stores
// every engine attribute as structured metadata of one stream (NEXORA_LOKI_SELECTOR). It talks to
// the HTTP API; every user value in a query is a quoted LogQL literal.
type Loki struct {
	client                *http.Client
	url, selector, tenant string
	username, password    string
	lookback              time.Duration
}

// NewLoki returns an adapter for cfg; the password, when configured, is read from its file.
func NewLoki(cfg config.LokiConfig) (*Loki, error) {
	var password string
	if cfg.PasswordFile != "" {
		raw, err := os.ReadFile(cfg.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("NEXORA_LOKI_PASSWORD_FILE: %w", err)
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}
	return &Loki{
		client: &http.Client{CheckRedirect: rejectBackendRedirect}, // every request carries the Search or Top deadline
		url:    strings.TrimRight(cfg.URL, "/"), selector: cmp.Or(cfg.Selector, lokiDefaultSelector),
		tenant: cfg.Tenant, username: cfg.Username, password: password,
		lookback: cmp.Or(cfg.Lookback, lokiDefaultLookback),
	}, nil
}

// Name implements Backend.
func (*Loki) Name() string { return "loki" }

var _ Topper = (*Loki)(nil)

// lokiHit is one log entry with its tiebreak key.
type lokiHit struct {
	ns  int64
	key string
	rec Record
}

// Search implements Backend. Entries are ordered by timestamp descending, then tiebreak key
// ascending; the cursor is base64url JSON ["<ns>", "<key>"] of the last record of the page.
func (l *Loki) Search(ctx context.Context, q Query) (Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	var cursorNs int64
	var cursorKey string
	hasCursor := q.Cursor != ""
	if hasCursor {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		var c []string
		if err != nil || json.Unmarshal(raw, &c) != nil || len(c) != 2 {
			return Page{}, ErrInvalidCursor
		}
		if cursorNs, err = strconv.ParseInt(c[0], 10, 64); err != nil {
			return Page{}, ErrInvalidCursor
		}
		cursorKey = c[1]
	}
	from, to := l.window(q.From, q.To)
	// Loki's end is exclusive; the filter below also drops anything outside [start, end).
	start, end := from.UnixNano(), to.UnixNano()+1
	if hasCursor {
		end = min(end, cursorNs+1)
	}
	ctx, cancel := context.WithTimeout(ctx, lokiSearchTimeout)
	defer cancel()
	params := url.Values{
		"query": {l.selector + lokiStages(q)}, "start": {strconv.FormatInt(start, 10)},
		"end": {strconv.FormatInt(end, 10)}, "direction": {"backward"},
	}
	for n := limit + 1; ; n = min(2*n, lokiMaxEntries) {
		params.Set("limit", strconv.Itoa(n))
		hits, err := l.logs(ctx, params)
		if err != nil {
			return Page{}, err
		}
		// A full response may cut the group of entries sharing its oldest timestamp: skip that group.
		full := len(hits) >= n
		oldest := int64(math.MaxInt64)
		for _, h := range hits {
			oldest = min(oldest, h.ns)
		}
		kept := hits[:0]
		for _, h := range hits {
			if h.ns < start || h.ns >= end || (hasCursor && h.ns == cursorNs && h.key <= cursorKey) || (full && h.ns == oldest) {
				continue
			}
			kept = append(kept, h)
		}
		slices.SortFunc(kept, func(a, b lokiHit) int { return cmp.Or(cmp.Compare(b.ns, a.ns), strings.Compare(a.key, b.key)) })
		switch {
		case len(kept) > limit || !full:
			return lokiPage(kept, limit), nil
		case n == lokiMaxEntries && len(kept) == 0:
			// debt: 5000 entries in one nanosecond is unreachable at microsecond engine timestamps; revisit if Loki raises max_entries_limit_per_query handling
			return Page{}, fmt.Errorf("%w: more than 5000 entries share one timestamp", ErrBackendUnavailable)
		case n == lokiMaxEntries:
			// Older entries exist beyond the skipped group: a short page continues from its last record.
			page := lokiPage(kept, len(kept))
			page.NextCursor = lokiCursor(kept[len(kept)-1])
			return page, nil
		}
	}
}

// lokiPage is the first limit hits, with a cursor when more follow.
func lokiPage(hits []lokiHit, limit int) Page {
	var page Page
	for _, h := range hits[:min(limit, len(hits))] {
		page.Records = append(page.Records, h.rec)
	}
	if len(hits) > limit {
		page.NextCursor = lokiCursor(hits[limit-1])
	}
	return page
}

func lokiCursor(h lokiHit) string {
	raw, _ := json.Marshal([]string{strconv.FormatInt(h.ns, 10), h.key})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// window resolves zero bounds: To is now, From is To minus the lookback.
func (l *Loki) window(from, to time.Time) (time.Time, time.Time) {
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-l.lookback)
	}
	return from, to
}

// Top implements Topper with instant metric queries in two steps, which keep ties exact: topk gives
// the k-th count c, then every key counted at least c is fetched and sorted by count, then key.
// When Loki refuses a step for its series limit, both steps run per partition of the key.
func (l *Loki) Top(ctx context.Context, q TopQuery) ([]TopEntry, error) {
	label, ok := lokiTopLabels[q.Field]
	if !ok {
		return nil, fmt.Errorf("top list field %q", q.Field)
	}
	from, to := l.window(q.From, q.To)
	if q.Limit <= 0 || from.After(to) {
		return []TopEntry{}, nil
	}
	// Loki range selectors and evaluation may round to milliseconds. Cover the window, then
	// filter on the original entry timestamp with integer Go-template comparisons. Comparing
	// epoch nanoseconds as LogQL numbers would lose precision through float64.
	end := to.Truncate(time.Millisecond).Add(time.Millisecond)
	start := from.Truncate(time.Millisecond).Add(-time.Millisecond)
	rangeMS := end.Sub(start).Milliseconds()
	at := strconv.FormatInt(end.UnixNano(), 10)
	window := fmt.Sprintf("{{ $t := (__timestamp__).UnixNano }}{{ and (ge $t %d) (le $t %d) }}", from.UnixNano(), to.UnixNano())
	stages := lokiStages(q.SearchQuery()) + " | label_format nexora_window=" + lokiQuote(window) + ` | nexora_window="true" | drop nexora_window`
	metric := func(partition string) string {
		extra := ""
		if partition != "" {
			extra = " | " + label + "=~" + lokiQuote(partition)
		}
		return fmt.Sprintf(`sum by (%s) (count_over_time(%s%s | %s!=""%s [%dms]))`, label, l.selector, stages, label, extra, rangeMS)
	}
	step1 := func(ctx context.Context, partition string) ([]TopEntry, error) {
		return l.vector(ctx, fmt.Sprintf("topk(%d, %s)", q.Limit, metric(partition)), label, at)
	}
	step2 := func(ctx context.Context, partition string, floor int64) ([]TopEntry, error) {
		return l.vector(ctx, fmt.Sprintf("%s >= %d", metric(partition), floor), label, at)
	}

	ctx, cancel := context.WithTimeout(ctx, lokiTopTimeout)
	defer cancel()
	candidates, err := step1(ctx, "")
	if err == nil && len(candidates) > 0 {
		candidates, err = step2(ctx, "", slices.MinFunc(candidates, byCount).Count)
	}
	if errors.Is(err, errLokiSeriesLimit) {
		candidates, err = topPartitioned(ctx, LokiPartitionRegexes(q.Field), q.Limit, step1, step2)
	}
	if err != nil {
		return nil, err
	}
	out := make([]TopEntry, 0, len(candidates))
	for _, e := range candidates {
		if e.Key != "" {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b TopEntry) int { return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Key, b.Key)) })
	return out[:min(len(out), q.Limit)], nil
}

func byCount(a, b TopEntry) int { return cmp.Compare(a.Count, b.Count) }

// topPartitioned runs step 1 in every partition regex, takes the k-th largest count across them as
// the floor, and runs step 2 in each partition that can reach it.
func topPartitioned(ctx context.Context, regexes []string, k int,
	step1 func(context.Context, string) ([]TopEntry, error),
	step2 func(context.Context, string, int64) ([]TopEntry, error),
) ([]TopEntry, error) {
	tops := make([][]TopEntry, len(regexes))
	if err := eachPartition(ctx, len(regexes), func(ctx context.Context, i int) (err error) {
		tops[i], err = step1(ctx, regexes[i])
		return err
	}); err != nil {
		return nil, err
	}
	var counts []int64
	for _, top := range tops {
		for _, e := range top {
			counts = append(counts, e.Count)
		}
	}
	if len(counts) == 0 {
		return nil, nil
	}
	slices.SortFunc(counts, func(a, b int64) int { return cmp.Compare(b, a) })
	floor := counts[min(k, len(counts))-1]
	found := make([][]TopEntry, len(regexes))
	if err := eachPartition(ctx, len(regexes), func(ctx context.Context, i int) (err error) {
		if len(tops[i]) == 0 || slices.MaxFunc(tops[i], byCount).Count < floor {
			return nil // no key of this partition reaches the floor
		}
		found[i], err = step2(ctx, regexes[i], floor)
		return err
	}); err != nil {
		return nil, err
	}
	return slices.Concat(found...), nil
}

// eachPartition runs fn for partitions 0..n-1, at most lokiPartitionConcurrency at a time, and
// returns the first error; a partition still over the series limit is ErrBackendUnavailable.
func eachPartition(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, lokiPartitionConcurrency)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for i := range n {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() {
					if errors.Is(err, errLokiSeriesLimit) {
						err = fmt.Errorf("%w: loki series limit: raise max_query_series", ErrBackendUnavailable)
					}
					first = err
					cancel()
				})
			}
		})
	}
	wg.Wait()
	return first
}

// logs runs a log query on /loki/api/v1/query_range and returns its entries.
func (l *Loki) logs(ctx context.Context, params url.Values) ([]lokiHit, error) {
	data, err := l.get(ctx, "/loki/api/v1/query_range", params, "streams")
	if err != nil {
		return nil, err
	}
	var streams []struct {
		Values [][]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(data, &streams); err != nil {
		return nil, fmt.Errorf("loki streams: %w", err)
	}
	var hits []lokiHit
	for _, s := range streams {
		for _, v := range s.Values {
			var ts, line string
			var meta struct {
				StructuredMetadata map[string]string `json:"structuredMetadata"`
			}
			if len(v) < 2 || json.Unmarshal(v[0], &ts) != nil || json.Unmarshal(v[1], &line) != nil ||
				(len(v) > 2 && json.Unmarshal(v[2], &meta) != nil) {
				return nil, fmt.Errorf("loki entry %s: unexpected shape", v)
			}
			ns, err := strconv.ParseInt(ts, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("loki entry timestamp %q: %w", ts, err)
			}
			md := meta.StructuredMetadata
			hits = append(hits, lokiHit{ns: ns, key: lokiKey(line, md), rec: recordFromLoki(ns, md)})
		}
	}
	return hits, nil
}

// vector runs an instant metric query at time at and returns its samples keyed by label.
func (l *Loki) vector(ctx context.Context, query, label, at string) ([]TopEntry, error) {
	data, err := l.get(ctx, "/loki/api/v1/query", url.Values{"query": {query}, "time": {at}}, "vector")
	if err != nil {
		return nil, err
	}
	var samples []struct {
		Metric map[string]string  `json:"metric"`
		Value  [2]json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		return nil, fmt.Errorf("loki vector: %w", err)
	}
	out := make([]TopEntry, 0, len(samples))
	for _, s := range samples {
		var v string
		if err := json.Unmarshal(s.Value[1], &v); err != nil {
			return nil, fmt.Errorf("loki sample %s: %w", s.Value[1], err)
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("loki sample %q: %w", v, err)
		}
		out = append(out, TopEntry{Key: s.Metric[label], Count: int64(f)})
	}
	return out, nil
}

// get runs a Loki API query and returns its data.result, which must be of resultType. Transport
// errors, HTTP 5xx and 429 are ErrBackendUnavailable; a series-limit 400 is errLokiSeriesLimit.
func (l *Loki) get(ctx context.Context, path string, params url.Values, resultType string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if l.username != "" {
		req.SetBasicAuth(l.username, l.password)
	}
	if l.tenant != "" {
		req.Header.Set("X-Scope-OrgID", l.tenant)
	}
	req.Header.Set("X-Loki-Response-Encoding-Flags", "categorize-labels")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: loki: %v", ErrBackendUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		text := strings.TrimSpace(string(body))
		switch {
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			return nil, fmt.Errorf("%w: loki HTTP %d: %s", ErrBackendUnavailable, resp.StatusCode, text)
		case resp.StatusCode == http.StatusBadRequest && (strings.Contains(text, "maximum number of series") || strings.Contains(text, "maximum of series")):
			return nil, fmt.Errorf("%w: %s", errLokiSeriesLimit, text)
		default:
			return nil, fmt.Errorf("loki %s HTTP %d: %s", path, resp.StatusCode, text)
		}
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: loki: %v", ErrBackendUnavailable, err)
		}
		return nil, fmt.Errorf("loki %s response: %w", path, err)
	}
	if out.Status != "success" || out.Data.ResultType != resultType {
		return nil, fmt.Errorf("loki %s: status %q, result type %q, want success and %s", path, out.Status, out.Data.ResultType, resultType)
	}
	return out.Data.Result, nil
}
