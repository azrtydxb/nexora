package querylog_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

const chRowID = "0b0c6f1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f"

func chRow(ns int64, rowid string) string {
	return fmt.Sprintf(`{"t":"%d","rowid":"%s","name":"a.test.","client":"10.0.0.1","qtype":"A","rcode":"NOERROR","cache":"miss","filter":"none","list_id":"","category":"","source":"","rule":"","policy_group":"","rpz_zone":"","rpz_action":"","acl_refused":"","upstreams_raced":0,"upstream":"fx","transport":"udp","engine_id":"e1","duration_us":77}`, ns, rowid)
}

// chRequest is one request the fake ClickHouse received.
type chRequest struct {
	query  url.Values
	sql    string
	user   string
	key    string
	method string
}

// fakeClickHouse answers every request with respond and records it.
func fakeClickHouse(t *testing.T, respond func(w http.ResponseWriter, r chRequest)) (*httptest.Server, *[]chRequest, *atomic.Int32) {
	t.Helper()
	var reqs []chRequest
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		b, _ := io.ReadAll(r.Body)
		req := chRequest{query: r.URL.Query(), sql: string(b), user: r.Header.Get("X-ClickHouse-User"), key: r.Header.Get("X-ClickHouse-Key"), method: r.Method}
		reqs = append(reqs, req)
		respond(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs, &n
}

func newTestClickHouse(t *testing.T, u string) *querylog.ClickHouse {
	t.Helper()
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch, err := querylog.NewClickHouse(config.ClickHouseConfig{URL: u, Database: "nexora", Table: "querylog", Username: "nexora_reader", PasswordFile: pw})
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func TestClickHouseQueryIsParameterised(t *testing.T) {
	const ns = int64(1757844000123456789)
	srv, reqs, _ := fakeClickHouse(t, func(w http.ResponseWriter, _ chRequest) {
		w.Header().Set("X-ClickHouse-Format", "JSONEachRow")
		_, _ = fmt.Fprintln(w, chRow(ns, chRowID))
		_, _ = fmt.Fprintln(w, chRow(ns-1, "1b0c6f1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f"))
	})
	ch := newTestClickHouse(t, srv.URL)
	if ch.Name() != "clickhouse" {
		t.Fatalf("name %q", ch.Name())
	}
	page, err := ch.Search(context.Background(), querylog.Query{
		Name: "a%b_c\\'); DROP", QTypes: []string{"A", "AAAA"}, PolicyGroups: []string{"global", "g1"}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests", len(*reqs))
	}
	r := (*reqs)[0]
	for _, want := range []string{"{name:String}", "QType IN {qtypes:Array(String)}", "ORDER BY Timestamp DESC, RowID ASC"} {
		if !strings.Contains(r.sql, want) {
			t.Errorf("sql lacks %q:\n%s", want, r.sql)
		}
	}
	if strings.Contains(r.sql, "DROP") || strings.Contains(r.sql, "a%b") || strings.Contains(r.sql, "g1") || strings.Contains(r.sql, "AAAA") {
		t.Errorf("a user value reached the SQL text:\n%s", r.sql)
	}
	// LIKE escaping (\ % _), then the escaped-format escaping ClickHouse applies to a String parameter.
	if got, want := r.query.Get("param_name"), `%a\\%b\\_c\\\\'); drop%`; got != want {
		t.Errorf("param_name %q, want %q", got, want)
	}
	if got := r.query.Get("param_qtypes"); got != "['A','AAAA']" {
		t.Errorf("param_qtypes %q", got)
	}
	if got := r.query.Get("param_policy_groups"); !strings.Contains(got, "''") || !strings.Contains(got, "'g1'") {
		t.Errorf("param_policy_groups %q", got)
	}
	if r.query.Get("database") != "nexora" || r.user != "nexora_reader" || r.key != "secret" || r.method != http.MethodPost {
		t.Errorf("database %q user %q key %q method %s", r.query.Get("database"), r.user, r.key, r.method)
	}
	if len(page.Records) != 1 || page.Records[0].Time.UnixNano() != ns || page.Records[0].DurationUS != 77 ||
		page.Records[0].Upstream != "fx" || page.NextCursor == "" {
		t.Fatalf("page %+v", page)
	}
}

func TestClickHouseArrayParamEscapesQuotes(t *testing.T) {
	srv, reqs, _ := fakeClickHouse(t, func(http.ResponseWriter, chRequest) {})
	ch := newTestClickHouse(t, srv.URL)
	if _, err := ch.Search(context.Background(), querylog.Query{ListIDs: []string{`a'b\c`}, Client: "x\ty"}); err != nil {
		t.Fatal(err)
	}
	q := (*reqs)[0].query
	if got := q.Get("param_list_ids"); got != `['a\'b\\c']` {
		t.Errorf("param_list_ids %q", got)
	}
	if got := q.Get("param_client"); got != `x\ty` {
		t.Errorf("param_client %q", got)
	}
}

func TestClickHouseCursorKeyset(t *testing.T) {
	const ns = int64(1757844000123456789)
	srv, reqs, n := fakeClickHouse(t, func(w http.ResponseWriter, _ chRequest) {
		_, _ = fmt.Fprintln(w, chRow(ns, chRowID))
		_, _ = fmt.Fprintln(w, chRow(ns, "1b0c6f1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f"))
	})
	ch := newTestClickHouse(t, srv.URL)
	page, err := ch.Search(context.Background(), querylog.Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
	if err != nil || string(raw) != fmt.Sprintf(`[%d,"%s"]`, ns, chRowID) {
		t.Fatalf("cursor %q -> %s %v", page.NextCursor, raw, err)
	}
	if strings.Contains((*reqs)[0].sql, "cursor_t") {
		t.Errorf("first page carries a keyset:\n%s", (*reqs)[0].sql)
	}
	if _, err := ch.Search(context.Background(), querylog.Query{Limit: 1, Cursor: page.NextCursor}); err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[1]
	if r.query.Get("param_cursor_t") != fmt.Sprint(ns) || r.query.Get("param_cursor_id") != chRowID {
		t.Errorf("cursor params %v", r.query)
	}
	const keyset = "(Timestamp < fromUnixTimestamp64Nano({cursor_t:Int64}) OR (Timestamp = fromUnixTimestamp64Nano({cursor_t:Int64}) AND RowID > {cursor_id:UUID}))"
	if !strings.Contains(r.sql, keyset) {
		t.Errorf("sql lacks the keyset:\n%s", r.sql)
	}
	before := n.Load()
	for _, c := range []string{
		"x",
		base64.RawURLEncoding.EncodeToString([]byte(`[1]`)),
		base64.RawURLEncoding.EncodeToString([]byte(`["a","b"]`)),
		base64.RawURLEncoding.EncodeToString([]byte(`[1,"not-a-uuid"]`)),
	} {
		if _, err := ch.Search(context.Background(), querylog.Query{Cursor: c}); !errors.Is(err, querylog.ErrInvalidCursor) {
			t.Errorf("cursor %q -> %v, want ErrInvalidCursor", c, err)
		}
	}
	if n.Load() != before {
		t.Errorf("invalid cursors sent %d requests", n.Load()-before)
	}
}

func TestClickHouseTopSQL(t *testing.T) {
	srv, reqs, n := fakeClickHouse(t, func(w http.ResponseWriter, _ chRequest) {
		_, _ = fmt.Fprintln(w, `{"key":"ads","c":"3"}`)
		_, _ = fmt.Fprintln(w, `{"key":"gambling","c":"1"}`)
	})
	ch := newTestClickHouse(t, srv.URL)
	from := time.Unix(0, 1757844000123456789)
	got, err := ch.Top(context.Background(), querylog.TopQuery{From: from, To: from.Add(time.Minute), Field: "category", Filters: []string{"blocked"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (querylog.TopEntry{Key: "ads", Count: 3}) || got[1] != (querylog.TopEntry{Key: "gambling", Count: 1}) {
		t.Fatalf("top %+v", got)
	}
	r := (*reqs)[0]
	for _, want := range []string{"Category AS key", "key != ''", "Filter IN {filters:Array(String)}", "ORDER BY c DESC, key ASC", "LIMIT {limit:UInt32}", "Timestamp >= fromUnixTimestamp64Nano({from:Int64})"} {
		if !strings.Contains(r.sql, want) {
			t.Errorf("sql lacks %q:\n%s", want, r.sql)
		}
	}
	if r.query.Get("param_limit") != "5" || r.query.Get("param_from") != "1757844000123456789" || strings.Contains(r.sql, "blocked") {
		t.Errorf("params %v sql %s", r.query, r.sql)
	}
	before := n.Load()
	if _, err := ch.Top(context.Background(), querylog.TopQuery{Field: "bogus", Limit: 5}); err == nil {
		t.Error("field bogus accepted")
	}
	if n.Load() != before {
		t.Error("field bogus sent a request")
	}
}

func TestClickHouseErrors(t *testing.T) {
	status, code := http.StatusOK, ""
	srv, _, _ := fakeClickHouse(t, func(w http.ResponseWriter, _ chRequest) {
		if code != "" {
			w.Header().Set("X-ClickHouse-Exception-Code", code)
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = fmt.Fprint(w, "Code: "+code+". DB::Exception: boom")
		}
	})
	ch := newTestClickHouse(t, srv.URL)
	if _, err := ch.Search(context.Background(), querylog.Query{}); err != nil {
		t.Fatalf("200: %v", err)
	}
	for _, tc := range []struct {
		status      int
		code        string
		unavailable bool
		mention     string
	}{
		{http.StatusServiceUnavailable, "", true, ""},
		{http.StatusTooManyRequests, "", true, ""},
		{http.StatusNotFound, "60", false, "apply deploy/clickhouse/querylog.sql"},
		{http.StatusNotFound, "81", false, "apply deploy/clickhouse/querylog.sql"},
		{http.StatusForbidden, "516", false, "NEXORA_CLICKHOUSE_PASSWORD_FILE"},
		{http.StatusBadRequest, "62", false, "DB::Exception: boom"},
	} {
		status, code = tc.status, tc.code
		_, err := ch.Search(context.Background(), querylog.Query{})
		if err == nil || errors.Is(err, querylog.ErrBackendUnavailable) != tc.unavailable || !strings.Contains(err.Error(), tc.mention) {
			t.Errorf("%d code %s -> %v, want unavailable=%v mentioning %q", tc.status, tc.code, err, tc.unavailable, tc.mention)
		}
		if _, err := ch.Top(context.Background(), querylog.TopQuery{Field: "name"}); err == nil || errors.Is(err, querylog.ErrBackendUnavailable) != tc.unavailable {
			t.Errorf("top %d code %s -> %v", tc.status, tc.code, err)
		}
	}
	srv.Close()
	if _, err := ch.Search(context.Background(), querylog.Query{}); !errors.Is(err, querylog.ErrBackendUnavailable) {
		t.Errorf("closed server -> %v", err)
	}
	if _, err := querylog.NewClickHouse(config.ClickHouseConfig{URL: srv.URL, Table: "querylog; DROP"}); err == nil {
		t.Error("table name with SQL accepted")
	}
	if _, err := querylog.NewClickHouse(config.ClickHouseConfig{URL: srv.URL, Table: "querylog", PasswordFile: "/nonexistent"}); err == nil || !strings.Contains(err.Error(), "NEXORA_CLICKHOUSE_PASSWORD_FILE") {
		t.Errorf("missing password file -> %v", err)
	}
}

func TestClickHouseSchemaHasAdapterColumns(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/clickhouse/querylog.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	cols := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\s*`?([A-Za-z_][A-Za-z0-9_.]*)`?\\s+[A-Z]").FindAllStringSubmatch(sql, -1) {
		cols[m[1]] = true
	}
	exporter := []string{"Timestamp", "TraceId", "SpanId", "TraceFlags", "SeverityText", "SeverityNumber", "ServiceName", "Body",
		"ResourceSchemaUrl", "ResourceAttributes", "ScopeSchemaUrl", "ScopeName", "ScopeVersion", "ScopeAttributes", "LogAttributes", "EventName", "RowID"}
	adapter := querylog.ClickHouseColumns()
	if len(adapter) < 20 {
		t.Fatalf("adapter columns %v", adapter)
	}
	for _, c := range append(exporter, adapter...) {
		if !cols[c] {
			t.Errorf("querylog.sql lacks column %s (has %v)", c, cols)
		}
	}
	if strings.Contains(sql, "create_schema") || strings.Count(sql, "IF NOT EXISTS") != 2 ||
		!strings.Contains(sql, "TTL toDateTime(Timestamp) + INTERVAL 7 DAY") || !strings.Contains(sql, "ngrambf_v1") {
		t.Error("querylog.sql misses IF NOT EXISTS twice, the 7-day TTL or the ngram index, or mentions create_schema")
	}
}
