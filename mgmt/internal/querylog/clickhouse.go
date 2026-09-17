package querylog

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/config"
)

const clickHouseTimeout = 5 * time.Second

var (
	clickHouseIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	clickHouseUUID  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ClickHouse searches the table deploy/clickhouse/querylog.sql defines, which the OpenTelemetry
// Collector's clickhouse exporter fills. It talks to the HTTP interface, and every user value is a
// typed query parameter ({name:Type} in the SQL, param_name in the URL).
type ClickHouse struct {
	client               *http.Client
	url, database, table string
	username, password   string
}

// NewClickHouse returns an adapter for cfg; the password, when configured, is read from its file.
func NewClickHouse(cfg config.ClickHouseConfig) (*ClickHouse, error) {
	if !clickHouseIdent.MatchString(cfg.Table) {
		return nil, fmt.Errorf("NEXORA_CLICKHOUSE_TABLE %q is not a plain identifier", cfg.Table)
	}
	var password string
	if cfg.PasswordFile != "" {
		raw, err := os.ReadFile(cfg.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("NEXORA_CLICKHOUSE_PASSWORD_FILE: %w", err)
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}
	return &ClickHouse{
		client: &http.Client{Timeout: clickHouseTimeout, CheckRedirect: rejectBackendRedirect},
		url:    strings.TrimRight(cfg.URL, "/"), database: cfg.Database, table: cfg.Table,
		username: cfg.Username, password: password,
	}, nil
}

// Name implements Backend.
func (*ClickHouse) Name() string { return "clickhouse" }

var _ Topper = (*ClickHouse)(nil)

// debt: materialized columns cover the post-M6 attributes only; a new engine attribute needs an ALTER TABLE ADD COLUMN in querylog.sql and an upgrade note
// clickHouseRecordColumns maps each JSON name of a search row to its table column.
var clickHouseRecordColumns = []struct{ json, column string }{
	{"name", "Name"}, {"client", "Client"}, {"qtype", "QType"}, {"rcode", "RCode"}, {"cache", "Cache"},
	{"filter", "Filter"}, {"list_id", "ListID"}, {"category", "Category"}, {"source", "Source"}, {"rule", "Rule"},
	{"policy_group", "PolicyGroupID"}, {"rpz_zone", "RPZZoneID"}, {"rpz_action", "RPZAction"},
	{"acl_refused", "ACLRefused"}, {"upstreams_raced", "UpstreamsRaced"}, {"upstream", "Upstream"},
	{"transport", "Transport"}, {"engine_id", "EngineID"}, {"duration_us", "DurationUS"},
}

// clickHouseTopColumns maps each top list field to its table column.
var clickHouseTopColumns = map[TopField]string{TopName: "Name", TopClient: "Client", TopCategory: "Category"}

// ClickHouseColumns returns every table column the adapter references (the schema test checks them
// against deploy/clickhouse/querylog.sql).
func ClickHouseColumns() []string {
	out := []string{"Timestamp", "RowID"}
	for _, c := range clickHouseRecordColumns {
		out = append(out, c.column)
	}
	return out
}

// chRow is one search row; integers arrive quoted or bare depending on the server's
// output_format_json_quote_64bit_integers.
type chRow struct {
	T              chInt  `json:"t"`
	RowID          string `json:"rowid"`
	Name           string `json:"name"`
	Client         string `json:"client"`
	QType          string `json:"qtype"`
	RCode          string `json:"rcode"`
	Cache          string `json:"cache"`
	Filter         string `json:"filter"`
	ListID         string `json:"list_id"`
	Category       string `json:"category"`
	Source         string `json:"source"`
	Rule           string `json:"rule"`
	PolicyGroup    string `json:"policy_group"`
	RPZZone        string `json:"rpz_zone"`
	RPZAction      string `json:"rpz_action"`
	ACLRefused     string `json:"acl_refused"`
	UpstreamsRaced chInt  `json:"upstreams_raced"`
	Upstream       string `json:"upstream"`
	Transport      string `json:"transport"`
	EngineID       string `json:"engine_id"`
	DurationUS     chInt  `json:"duration_us"`
}

// chInt is a 64-bit integer that ClickHouse writes as a JSON number or a decimal string.
type chInt int64

// UnmarshalJSON accepts 77 and "77".
func (n *chInt) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	*n = chInt(v)
	return err
}

// Search implements Backend: newest first, RowID ascending within one nanosecond, with a
// base64url JSON [timestamp_ns, "rowid"] keyset cursor.
func (c *ClickHouse) Search(ctx context.Context, q Query) (Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	clauses, params := clickHouseWhere(q)
	if q.Cursor != "" {
		ns, id, ok := decodeClickHouseCursor(q.Cursor)
		if !ok {
			return Page{}, ErrInvalidCursor
		}
		clauses = append(clauses, "(Timestamp < fromUnixTimestamp64Nano({cursor_t:Int64}) OR (Timestamp = fromUnixTimestamp64Nano({cursor_t:Int64}) AND RowID > {cursor_id:UUID}))")
		params["cursor_t"], params["cursor_id"] = strconv.FormatInt(ns, 10), id
	}
	params["limit"] = strconv.Itoa(limit + 1)
	cols := []string{"toString(toUnixTimestamp64Nano(Timestamp)) AS t", "toString(RowID) AS rowid"}
	for _, col := range clickHouseRecordColumns {
		cols = append(cols, col.column+" AS "+col.json)
	}
	sql := "SELECT " + strings.Join(cols, ", ") + " FROM `" + c.table + "`" + whereSQL(clauses) +
		" ORDER BY Timestamp DESC, RowID ASC LIMIT {limit:UInt32} FORMAT JSONEachRow"
	var rows []chRow
	err := c.do(ctx, sql, params, func(dec *json.Decoder) error {
		var r chRow
		if err := dec.Decode(&r); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	var page Page
	for i, r := range rows {
		if i == limit {
			last := rows[i-1]
			raw, err := json.Marshal([]any{int64(last.T), last.RowID})
			if err != nil {
				return Page{}, err
			}
			page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		page.Records = append(page.Records, Record{
			Time: time.Unix(0, int64(r.T)).UTC(), Client: r.Client, Name: r.Name, QType: r.QType, RCode: r.RCode,
			Cache: r.Cache, Filter: r.Filter, Upstream: r.Upstream, Transport: r.Transport, EngineID: r.EngineID,
			ListID: r.ListID, Category: r.Category, Source: r.Source, Rule: r.Rule, PolicyGroupID: r.PolicyGroup,
			RPZZoneID: r.RPZZone, RPZAction: r.RPZAction, ACLRefused: r.ACLRefused,
			UpstreamsRaced: int64(r.UpstreamsRaced), DurationUS: int64(r.DurationUS),
		})
	}
	return page, nil
}

// decodeClickHouseCursor parses a base64url JSON [timestamp_ns, "rowid"] cursor.
func decodeClickHouseCursor(cursor string) (int64, string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "", false
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) != 2 {
		return 0, "", false
	}
	var ns int64
	var id string
	if json.Unmarshal(parts[0], &ns) != nil || json.Unmarshal(parts[1], &id) != nil || !clickHouseUUID.MatchString(id) {
		return 0, "", false
	}
	return ns, id, true
}

// Top implements Topper with an exact GROUP BY under the same time and filter clauses as Search.
func (c *ClickHouse) Top(ctx context.Context, q TopQuery) ([]TopEntry, error) {
	col, ok := clickHouseTopColumns[q.Field]
	if !ok {
		return nil, fmt.Errorf("top list field %q", q.Field)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	clauses, params := clickHouseWhere(Query{From: q.From, To: q.To, Filters: q.Filters})
	clauses = append(clauses, "key != ''")
	params["limit"] = strconv.Itoa(limit)
	sql := "SELECT " + col + " AS key, count() AS c FROM `" + c.table + "`" + whereSQL(clauses) +
		" GROUP BY key ORDER BY c DESC, key ASC LIMIT {limit:UInt32} FORMAT JSONEachRow"
	out := []TopEntry{}
	err := c.do(ctx, sql, params, func(dec *json.Decoder) error {
		var r struct {
			Key string `json:"key"`
			C   chInt  `json:"c"`
		}
		if err := dec.Decode(&r); err != nil {
			return err
		}
		out = append(out, TopEntry{Key: r.Key, Count: int64(r.C)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// clickHouseWhere builds q's time and field clauses (limit and cursor aside) with their parameters.
func clickHouseWhere(q Query) ([]string, map[string]string) {
	var clauses []string
	params := map[string]string{}
	if !q.From.IsZero() {
		clauses = append(clauses, "Timestamp >= fromUnixTimestamp64Nano({from:Int64})")
		params["from"] = strconv.FormatInt(q.From.UnixNano(), 10)
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "Timestamp <= fromUnixTimestamp64Nano({to:Int64})")
		params["to"] = strconv.FormatInt(q.To.UnixNano(), 10)
	}
	if q.Client != "" {
		clauses = append(clauses, "Client = {client:String}")
		params["client"] = escapeClickHouseParam(q.Client)
	}
	if name := strings.TrimSuffix(q.Name, "."); name != "" {
		clauses = append(clauses, "lower(Name) LIKE {name:String}")
		params["name"] = escapeClickHouseParam(likeParam(name))
	}
	groups := make([]string, len(q.PolicyGroups))
	for i, g := range q.PolicyGroups {
		if g != GlobalPolicyGroup {
			groups[i] = g
		}
	}
	for _, f := range []struct {
		column, param string
		values        []string
	}{
		{"QType", "qtypes", q.QTypes}, {"RCode", "rcodes", q.RCodes}, {"Cache", "caches", q.Caches},
		{"Filter", "filters", q.Filters}, {"Category", "categories", q.Categories}, {"Source", "sources", q.Sources},
		{"ListID", "list_ids", q.ListIDs}, {"EngineID", "engine_ids", q.EngineIDs}, {"PolicyGroupID", "policy_groups", groups},
	} {
		if len(f.values) > 0 {
			clauses = append(clauses, f.column+" IN {"+f.param+":Array(String)}")
			params[f.param] = arrayParam(f.values)
		}
	}
	return clauses, params
}

func whereSQL(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(clauses, " AND ")
}

// likeParam is the LIKE pattern matching fragment as a case-insensitive substring (the fragment is
// lowercased; the column is lower(Name)) with \, % and _ literal.
func likeParam(fragment string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(fragment)) + "%"
}

// escapeClickHouseParam escapes a scalar parameter value for ClickHouse, which parses it in the
// escaped (TSV) format: \ starts an escape sequence, and a raw tab or newline ends the value.
func escapeClickHouseParam(v string) string {
	return strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`).Replace(v)
}

// arrayParam is an Array(String) parameter literal: ['a','b'] with \ and ' escaped.
func arrayParam(values []string) string {
	esc := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = "'" + esc.Replace(v) + "'"
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// do POSTs sql with params and hands the response body to out once per JSONEachRow row. Transport
// errors, HTTP 5xx and 429 are ErrBackendUnavailable; other failures are plain errors carrying
// ClickHouse's message.
func (c *ClickHouse) do(ctx context.Context, sql string, params map[string]string, out func(dec *json.Decoder) error) error {
	v := url.Values{"database": {c.database}}
	for k, p := range params {
		v.Set("param_"+k, p)
	}
	ctx, cancel := context.WithTimeout(ctx, clickHouseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/?"+v.Encode(), bytes.NewReader([]byte(sql)))
	if err != nil {
		return err
	}
	req.Header.Set("X-ClickHouse-User", c.username)
	if c.password != "" {
		req.Header.Set("X-ClickHouse-Key", c.password)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: clickhouse: %v", ErrBackendUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("%w: clickhouse HTTP %d: %s", ErrBackendUnavailable, resp.StatusCode, bytes.TrimSpace(msg))
		}
		hint := ""
		switch resp.Header.Get("X-ClickHouse-Exception-Code") {
		case "60", "81":
			hint = " (apply deploy/clickhouse/querylog.sql)"
		case "516":
			hint = " (check NEXORA_CLICKHOUSE_PASSWORD_FILE)"
		}
		return fmt.Errorf("clickhouse HTTP %d%s: %s", resp.StatusCode, hint, bytes.TrimSpace(msg))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		if err := out(dec); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if ctx.Err() != nil {
				return fmt.Errorf("%w: clickhouse: %v", ErrBackendUnavailable, err)
			}
			return fmt.Errorf("clickhouse response: %w", err)
		}
	}
}
