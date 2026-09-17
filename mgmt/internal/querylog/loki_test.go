package querylog_test

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

// lokiReq is what the fake Loki records of one request.
type lokiReq struct {
	Path, Query, Start, End, Time, Limit, Direction string
	Header                                          http.Header
}

// fakeLoki is an httptest Loki that answers every request with answer.
type fakeLoki struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []lokiReq
}

func newFakeLoki(t *testing.T, answer func(w http.ResponseWriter, r lokiReq)) *fakeLoki {
	t.Helper()
	f := &fakeLoki{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query()
		req := lokiReq{Path: r.URL.Path, Query: p.Get("query"), Start: p.Get("start"), End: p.Get("end"), Time: p.Get("time"),
			Limit: p.Get("limit"), Direction: p.Get("direction"), Header: r.Header.Clone()}
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		answer(w, req)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeLoki) requests() []lokiReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// lokiEntry is one log entry of the fake.
type lokiEntry struct {
	ns   int64
	line string
	md   map[string]string
}

// streamsJSON renders entries as one stream in the categorize-labels encoding.
func streamsJSON(entries []lokiEntry) string {
	values := make([][]any, 0, len(entries))
	for _, e := range entries {
		values = append(values, []any{strconv.FormatInt(e.ns, 10), e.line, map[string]any{"structuredMetadata": e.md}})
	}
	raw, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{
		"resultType": "streams", "encodingFlags": []string{"categorize-labels"},
		"result": []any{map[string]any{"stream": map[string]string{"service_name": "nexora-engine"}, "values": values}},
	}})
	return string(raw)
}

// vectorJSON renders entries as an instant vector keyed by label.
func vectorJSON(label string, entries []querylog.TopEntry) string {
	result := make([]any, 0, len(entries))
	for _, e := range entries {
		result = append(result, map[string]any{"metric": map[string]string{label: e.Key}, "value": []any{1757844000.0, strconv.FormatInt(e.Count, 10)}})
	}
	raw, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	return string(raw)
}

// tiebreakKey is the documented Loki tiebreak key: the hex SHA-256 of the line and the sorted
// structured metadata.
func tiebreakKey(line string, md map[string]string) string {
	h := sha256.New()
	h.Write([]byte(line))
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range md {
			if !yield(k) {
				return
			}
		}
	}) {
		h.Write([]byte("\x00" + k + "=" + md[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newLoki(t *testing.T, cfg config.LokiConfig) *querylog.Loki {
	t.Helper()
	l, err := querylog.NewLoki(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

const engineSelector = `{service_name="nexora-engine"}`

func TestLokiLogQLQuotesValues(t *testing.T) {
	to := time.Date(2026, 9, 14, 10, 0, 0, 123456789, time.UTC)
	from := to.Add(-time.Hour)
	f := newFakeLoki(t, func(w http.ResponseWriter, _ lokiReq) {
		_, _ = fmt.Fprint(w, streamsJSON([]lokiEntry{{ns: to.UnixNano(), line: "10.0.0.1 a(b).c. A udp e1", md: map[string]string{
			"client_address": "10.0.0.1", "dns_question_name": "a(b).c.", "dns_question_type": "A", "dns_response_code": "NOERROR",
			"nexora_cache": "miss", "nexora_filter_result": "blocked", "nexora_policy_group": "g1", "nexora_rpz": "none",
			"nexora_duration_us": "77", "nexora_upstream_raced": "2", "nexora_engine_id": "e1", "nexora_transport": "udp",
			"nexora_filter_list_id": "l1", "nexora_filter_category": "ads", "nexora_filter_source": "blocklist",
			"nexora_filter_rule": "a(b).c", "nexora_rpz_zone": "z", "nexora_acl_refused": "", "nexora_upstream": "fx",
		}}}))
	})
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newLoki(t, config.LokiConfig{URL: f.URL, Selector: engineSelector, Tenant: "t1", Username: "u", PasswordFile: pw, Lookback: 168 * time.Hour})
	if l.Name() != "loki" {
		t.Fatalf("name %q", l.Name())
	}
	page, err := l.Search(context.Background(), querylog.Query{
		From: from, To: to, Name: "A(b).c.", QTypes: []string{"A", "AA|AA"}, PolicyGroups: []string{"global", "g1"},
		Filters: []string{"blocked"}, Client: "10.0.0.1", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := f.requests()[0]
	for _, want := range []string{
		engineSelector,
		`dns_question_name=~"(?i)^.*a\\(b\\)\\.c.*$"`,
		`dns_question_type=~"^(?:A|AA\\|AA)$"`,
		`nexora_policy_group=~"^(?:|g1)$"`,
		`(nexora_filter=~"^(?:blocked)$" or nexora_filter_result=~"^(?:blocked)$")`,
		`client_address="10.0.0.1"`,
	} {
		if !strings.Contains(r.Query, want) {
			t.Errorf("query %s lacks %s", r.Query, want)
		}
	}
	if r.Path != "/loki/api/v1/query_range" || r.Start != strconv.FormatInt(from.UnixNano(), 10) ||
		r.End != strconv.FormatInt(to.UnixNano()+1, 10) || r.Direction != "backward" || r.Limit != "11" {
		t.Errorf("request %+v", r)
	}
	if r.Header.Get("X-Scope-OrgID") != "t1" || r.Header.Get("X-Loki-Response-Encoding-Flags") != "categorize-labels" {
		t.Errorf("headers %v", r.Header)
	}
	if u, p, ok := (&http.Request{Header: r.Header}).BasicAuth(); !ok || u != "u" || p != "s3cret" {
		t.Errorf("basic auth %q %q %v", u, p, ok)
	}
	want := querylog.Record{
		Time: to, Client: "10.0.0.1", Name: "a(b).c.", QType: "A", RCode: "NOERROR", Cache: "miss", Filter: "blocked",
		Upstream: "fx", Transport: "udp", EngineID: "e1", ListID: "l1", Category: "ads", Source: "blocklist", Rule: "a(b).c",
		PolicyGroupID: "g1", RPZZoneID: "z", UpstreamsRaced: 2, DurationUS: 77,
	}
	if len(page.Records) != 1 || page.Records[0] != want || page.NextCursor != "" {
		t.Fatalf("page %+v, want %+v", page, want)
	}

	// A user value that tries to close the string and add a stage stays one quoted literal.
	evil := "x\" or client_address=~\".*\n"
	if _, err := l.Search(context.Background(), querylog.Query{From: from, To: to, Name: evil, Client: evil}); err != nil {
		t.Fatal(err)
	}
	q := f.requests()[1].Query
	rest := q
	for _, want := range []string{
		`dns_question_name=~` + strconv.Quote("(?i)^.*"+regexp.QuoteMeta(strings.ToLower(evil))+".*$"),
		`client_address=` + strconv.Quote(evil),
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query %s lacks %s", q, want)
		}
		rest = strings.Replace(rest, want, "", 1)
	}
	// Without the two quoted literals nothing of the user value is left in the query.
	if strings.Contains(q, "\n") || strings.Contains(rest, "client_address=~") || strings.Contains(rest, " or ") {
		t.Errorf("unquoted user value in %s", q)
	}

	if _, err := l.Search(context.Background(), querylog.Query{To: to}); err != nil {
		t.Fatal(err)
	}
	if r := f.requests()[2]; r.Start != strconv.FormatInt(to.Add(-168*time.Hour).UnixNano(), 10) || r.Query != engineSelector {
		t.Errorf("zero From: %+v, want start To-168h and the bare selector", r)
	}
}

func TestLokiPagingTiesAtOneNanosecond(t *testing.T) {
	const tie = int64(1757844000123456789)
	var held []lokiEntry
	for i := range 5 {
		held = append(held, lokiEntry{ns: tie, line: fmt.Sprintf("10.0.0.%d tie.test. A udp e1", i), md: map[string]string{"client_address": fmt.Sprintf("10.0.0.%d", i)}})
	}
	held = append(held,
		lokiEntry{ns: tie - 1000, line: "10.0.1.1 old.test. A udp e1", md: map[string]string{"client_address": "10.0.1.1"}},
		lokiEntry{ns: tie - 2000, line: "10.0.1.2 old.test. A udp e1", md: map[string]string{"client_address": "10.0.1.2"}},
	)
	// answer returns the newest limit entries with ts < end, ts descending, then insertion order.
	answer := func(entries []lokiEntry) func(w http.ResponseWriter, r lokiReq) {
		return func(w http.ResponseWriter, r lokiReq) {
			limit, _ := strconv.Atoi(r.Limit)
			end, _ := strconv.ParseInt(r.End, 10, 64)
			var out []lokiEntry
			for _, e := range entries {
				if e.ns < end {
					out = append(out, e)
				}
			}
			slices.SortStableFunc(out, func(a, b lokiEntry) int { return cmp.Compare(b.ns, a.ns) })
			_, _ = fmt.Fprint(w, streamsJSON(out[:min(limit, len(out))]))
		}
	}
	f := newFakeLoki(t, answer(held))
	l := newLoki(t, config.LokiConfig{URL: f.URL})
	q := querylog.Query{From: time.Unix(0, tie).Add(-time.Hour), To: time.Unix(0, tie), Limit: 2}
	var got []querylog.Record
	for range 20 {
		p, err := l.Search(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Records) > 2 {
			t.Fatalf("page of %d", len(p.Records))
		}
		got = append(got, p.Records...)
		if p.NextCursor == "" {
			break
		}
		q.Cursor = p.NextCursor
	}
	clients := map[string]bool{}
	var tieKeys []string
	for _, r := range got {
		if clients[r.Client] {
			t.Errorf("duplicate record %+v", r)
		}
		clients[r.Client] = true
		if r.Time.UnixNano() == tie {
			tieKeys = append(tieKeys, tiebreakKey(fmt.Sprintf("%s tie.test. A udp e1", r.Client), map[string]string{"client_address": r.Client}))
		}
	}
	if len(got) != 7 || len(clients) != 7 || len(tieKeys) != 5 || !slices.IsSorted(tieKeys) {
		t.Fatalf("records %+v (tie keys %v), want 7 distinct with the 5 ties in ascending key order", got, tieKeys)
	}
	if !slices.ContainsFunc(f.requests(), func(r lokiReq) bool { n, _ := strconv.Atoi(r.Limit); return n > 3 }) {
		t.Error("no request grew beyond limit+1 although the first response cut the tie group")
	}

	var crowd []lokiEntry
	for i := range 5001 {
		crowd = append(crowd, lokiEntry{ns: tie, line: fmt.Sprintf("c%d", i), md: map[string]string{}})
	}
	f = newFakeLoki(t, answer(crowd))
	l = newLoki(t, config.LokiConfig{URL: f.URL})
	_, err := l.Search(context.Background(), querylog.Query{To: time.Unix(0, tie), Limit: 2})
	if !errors.Is(err, querylog.ErrBackendUnavailable) || !strings.Contains(err.Error(), "more than 5000 entries share one timestamp") {
		t.Fatalf("5001 entries in one ns: %v", err)
	}
}

func TestLokiTopTwoStepTies(t *testing.T) {
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Minute)
	e := func(k string, n int64) querylog.TopEntry { return querylog.TopEntry{Key: k, Count: n} }
	f := newFakeLoki(t, func(w http.ResponseWriter, r lokiReq) {
		if strings.HasPrefix(r.Query, "topk(") {
			_, _ = fmt.Fprint(w, vectorJSON("dns_question_name", []querylog.TopEntry{e("x", 3), e("c", 4)}))
			return
		}
		_, _ = fmt.Fprint(w, vectorJSON("dns_question_name", []querylog.TopEntry{e("b", 3), e("x", 3), e("c", 4)}))
	})
	l := newLoki(t, config.LokiConfig{URL: f.URL})
	got, err := l.Top(context.Background(), querylog.TopQuery{From: from, To: to, Field: querylog.TopName, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []querylog.TopEntry{e("c", 4), e("b", 3)}) {
		t.Fatalf("top %v", got)
	}
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	// Loki ranges are whole milliseconds and left-open: 60001ms evaluated at From-1ns+60001ms covers
	// exactly (From-1ns, To+1ms-1ns].
	rangeMS := int64(60_001)
	for _, want := range []string{`topk(2, sum by (dns_question_name) (count_over_time(`, `dns_question_name!=""`, fmt.Sprintf("[%dms]", rangeMS)} {
		if !strings.Contains(reqs[0].Query, want) {
			t.Errorf("step 1 %s lacks %s", reqs[0].Query, want)
		}
	}
	if wantTime := strconv.FormatInt(from.UnixNano()-1+rangeMS*int64(time.Millisecond), 10); reqs[0].Path != "/loki/api/v1/query" || reqs[0].Time != wantTime {
		t.Errorf("step 1 %+v, want time %s", reqs[0], wantTime)
	}
	if !strings.HasSuffix(reqs[1].Query, ") >= 3") || strings.HasPrefix(reqs[1].Query, "topk(") {
		t.Errorf("step 2 %s", reqs[1].Query)
	}

	f = newFakeLoki(t, func(w http.ResponseWriter, _ lokiReq) { _, _ = fmt.Fprint(w, vectorJSON("dns_question_name", nil)) })
	l = newLoki(t, config.LokiConfig{URL: f.URL})
	got, err = l.Top(context.Background(), querylog.TopQuery{From: from, To: to, Field: querylog.TopName, Limit: 2})
	if err != nil || got == nil || len(got) != 0 || len(f.requests()) != 1 {
		t.Fatalf("empty window: %v %v after %d requests, want [] after 1", got, err, len(f.requests()))
	}
}

func TestLokiTopPartitionFallback(t *testing.T) {
	const seriesLimit = `maximum number of series (500) reached for a single query; consider reducing query cardinality`
	counts := map[string]int64{}
	for i := range 200 {
		// First characters over all 37 name partitions, half of them behind www.
		key := fmt.Sprintf("%cq%d.test.", "0123456789abcdefghijklmnopqrstuvwxyz-"[i%37], i)
		if i%2 == 0 {
			key = "www." + key
		}
		counts[key] = 1 + int64(i%7)
	}
	partition := regexp.MustCompile(`dns_question_name=~("(?:[^"\\]|\\.)*")`)
	topk := regexp.MustCompile(`^topk\((\d+), `)
	threshold := regexp.MustCompile(` >= (\d+)$`)
	var inFlight, maxInFlight, step1 atomic.Int64
	fake := func(rejectPartitions bool) *fakeLoki {
		return newFakeLoki(t, func(w http.ResponseWriter, r lokiReq) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for m := maxInFlight.Load(); n > m && !maxInFlight.CompareAndSwap(m, n); m = maxInFlight.Load() {
			}
			time.Sleep(10 * time.Millisecond)
			m := partition.FindStringSubmatch(r.Query)
			var re *regexp.Regexp
			if m != nil {
				pattern, err := strconv.Unquote(m[1])
				if err != nil || !strings.HasPrefix(pattern, "(?is)^") {
					m = nil
				} else {
					re = regexp.MustCompile(pattern)
				}
			}
			if m == nil || rejectPartitions {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, seriesLimit)
				return
			}
			var out []querylog.TopEntry
			for k, c := range counts {
				if re.MatchString(k) {
					out = append(out, querylog.TopEntry{Key: k, Count: c})
				}
			}
			slices.SortFunc(out, func(a, b querylog.TopEntry) int {
				return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Key, b.Key))
			})
			if k := topk.FindStringSubmatch(r.Query); k != nil {
				step1.Add(1)
				n, _ := strconv.Atoi(k[1])
				out = out[:min(n, len(out))]
			} else if c := threshold.FindStringSubmatch(r.Query); c != nil {
				floor, _ := strconv.ParseInt(c[1], 10, 64)
				out = slices.DeleteFunc(out, func(e querylog.TopEntry) bool { return e.Count < floor })
			} else {
				t.Errorf("unexpected query %s", r.Query)
			}
			_, _ = fmt.Fprint(w, vectorJSON("dns_question_name", out))
		})
	}
	f := fake(false)
	l := newLoki(t, config.LokiConfig{URL: f.URL})
	q := querylog.TopQuery{From: time.Now().Add(-time.Hour), To: time.Now(), Field: querylog.TopName, Limit: 10}
	got, err := l.Top(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	var want []querylog.TopEntry
	for k, c := range counts {
		want = append(want, querylog.TopEntry{Key: k, Count: c})
	}
	slices.SortFunc(want, func(a, b querylog.TopEntry) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Key, b.Key))
	})
	if !slices.Equal(got, want[:10]) {
		t.Fatalf("top %v, want %v", got, want[:10])
	}
	if n := maxInFlight.Load(); n > 6 || n < 2 {
		t.Errorf("%d partition queries in flight at most, want 2..6", n)
	}
	if n := step1.Load(); n != 37 {
		t.Errorf("%d step-1 partition queries, want 37", n)
	}

	f = fake(true)
	l = newLoki(t, config.LokiConfig{URL: f.URL})
	_, err = l.Top(context.Background(), q)
	if !errors.Is(err, querylog.ErrBackendUnavailable) || !strings.Contains(err.Error(), "loki series limit: raise max_query_series") {
		t.Fatalf("partition over the limit: %v", err)
	}
}

func TestLokiPartitionsCoverEveryKey(t *testing.T) {
	// matching returns the indexes of the partitions of field that key falls in.
	compiled := map[querylog.TopField][]*regexp.Regexp{}
	for _, field := range []querylog.TopField{querylog.TopName, querylog.TopClient, querylog.TopCategory} {
		for _, p := range querylog.LokiPartitionRegexes(field) {
			compiled[field] = append(compiled[field], regexp.MustCompile(p))
		}
		if n := len(compiled[field]); n != 37 {
			t.Fatalf("%s: %d partitions, want 37", field, n)
		}
	}
	matching := func(_ *testing.T, field querylog.TopField, key string) []int {
		var out []int
		for i, re := range compiled[field] {
			if re.MatchString(key) {
				out = append(out, i)
			}
		}
		return out
	}
	// keys are fixed samples plus 10,000 random strings, a third of them behind a "www." prefix.
	keys := func(samples ...string) []string {
		alphabet := []rune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ.:-_éwW")
		rng := rand.New(rand.NewPCG(1, 2))
		for range 10_000 {
			k := make([]rune, rng.IntN(21))
			for i := range k {
				k[i] = alphabet[rng.IntN(len(alphabet))]
			}
			key := string(k)
			if rng.IntN(3) == 0 {
				key = []string{"www.", "WWW.", "ww", "www"}[rng.IntN(4)] + key
			}
			samples = append(samples, key)
		}
		return samples
	}

	t.Run("names by first character after www.", func(t *testing.T) {
		// Positive path first: sample names land in the partition of their first character.
		const a, g, t1, w, x, none = 10, 16, 1, 32, 33, 36
		for key, want := range map[string]int{
			"a.test.": a, "Google.com.": g, "www.google.com.": g, "WWW.Google.COM": g, "1.1.1.1.in-addr.arpa.": t1,
			"xn--bcher-kva.example.": x, "www.xn--bcher-kva.example.": x, "w.com.": w, "wwwx.com.": w, "ww.com.": w,
			"www.w.com.": w, "www.www.a.": w, "www": w, ".": none, "": none, "www.": none, "www..": none,
			"_dmarc.example.com.": none, "www._sip.example.": none, "-x.": none, "é.": none,
		} {
			if got := matching(t, querylog.TopName, key); !slices.Equal(got, []int{want}) {
				t.Errorf("name %q is in partitions %v, want [%d]", key, got, want)
			}
		}
		for _, k := range keys() {
			if got := matching(t, querylog.TopName, k); len(got) != 1 {
				t.Errorf("name %q is in partitions %v, want exactly one", k, got)
			}
		}
	})

	t.Run("clients and categories by last character", func(t *testing.T) {
		if got := matching(t, querylog.TopClient, "10.0.0.13"); !slices.Equal(got, []int{3}) {
			t.Fatalf("10.0.0.13 is in partitions %v, want [3]", got)
		}
		for _, k := range keys("", ".", "a.", "A", "::1", "fe80::a", "x-", "é", "a..", "..", "Z.") {
			for _, field := range []querylog.TopField{querylog.TopClient, querylog.TopCategory} {
				if got := matching(t, field, k); len(got) != 1 {
					t.Errorf("%s %q is in partitions %v, want exactly one", field, k, got)
				}
			}
		}
	})
}

func TestLokiErrors(t *testing.T) {
	status, body := http.StatusOK, streamsJSON(nil)
	f := newFakeLoki(t, func(w http.ResponseWriter, _ lokiReq) {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	})
	l := newLoki(t, config.LokiConfig{URL: f.URL})
	ctx := context.Background()
	if _, err := l.Search(ctx, querylog.Query{}); err != nil {
		t.Fatalf("200: %v", err)
	}
	for _, code := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		status, body = code, "busy"
		if _, err := l.Search(ctx, querylog.Query{}); !errors.Is(err, querylog.ErrBackendUnavailable) {
			t.Errorf("HTTP %d: %v, want ErrBackendUnavailable", code, err)
		}
	}
	status, body = http.StatusBadRequest, "parse error at line 1, col 2: syntax error"
	if _, err := l.Search(ctx, querylog.Query{}); err == nil || errors.Is(err, querylog.ErrBackendUnavailable) || !strings.Contains(err.Error(), "parse error") {
		t.Errorf("HTTP 400: %v, want a plain error with the parse error", err)
	}
	before := len(f.requests())
	for _, c := range []string{
		"x",
		base64.RawURLEncoding.EncodeToString([]byte(`[1]`)),
		base64.RawURLEncoding.EncodeToString([]byte(`["a","b"]`)),
		base64.RawURLEncoding.EncodeToString([]byte(`["1"]`)),
		base64.RawURLEncoding.EncodeToString([]byte(`[1757844000123456789,"0b0c"]`)),
	} {
		if _, err := l.Search(ctx, querylog.Query{Cursor: c}); !errors.Is(err, querylog.ErrInvalidCursor) {
			t.Errorf("cursor %q: %v, want ErrInvalidCursor", c, err)
		}
	}
	if n := len(f.requests()); n != before {
		t.Errorf("invalid cursors sent %d requests", n-before)
	}
	if _, err := l.Top(ctx, querylog.TopQuery{Field: "bogus", Limit: 1}); err == nil {
		t.Error("top field bogus accepted")
	}
	f.Close()
	if _, err := l.Search(ctx, querylog.Query{}); !errors.Is(err, querylog.ErrBackendUnavailable) {
		t.Errorf("closed server: %v, want ErrBackendUnavailable", err)
	}
	if _, err := querylog.NewLoki(config.LokiConfig{URL: "http://l", PasswordFile: "/nonexistent"}); err == nil || !strings.Contains(err.Error(), "NEXORA_LOKI_PASSWORD_FILE") {
		t.Errorf("unreadable password file: %v", err)
	}
}
