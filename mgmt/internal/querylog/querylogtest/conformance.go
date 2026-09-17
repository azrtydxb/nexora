package querylogtest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

// Harness adapts one backend to the suite. Ingest delivers one batch attributed to engineID
// (builtin: Builtin.Ingest; others: OTLP export to a collector, the dataset carrying engineID as
// the nexora.engine.id log and resource attribute). Visible blocks until want in-window records
// are searchable.
type Harness struct {
	Backend querylog.Backend
	Ingest  func(t testing.TB, engineID string, req *collogspb.ExportLogsServiceRequest)
	Visible func(t testing.TB, want int)
}

const (
	// maxPages bounds a cursor walk so a backend that never ends paging fails instead of hanging.
	maxPages = 200
	// pollInterval and pollTimeout bound PollVisible.
	pollInterval = 250 * time.Millisecond
	pollTimeout  = 60 * time.Second
)

// pageLimits are the page sizes every search case is walked at.
var pageLimits = []int{1, 2, 7, 1000}

// Run ingests Dataset(run, base) into h and into a builtin reference, then checks that every
// conformance query returns the same records and top lists from h as from the reference.
func Run(tb testing.TB, run string, base time.Time, h Harness) {
	tb.Helper()
	batches := Dataset(run, base)
	ref := querylog.NewBuiltin(10_000)
	for _, b := range batches {
		h.Ingest(tb, b.EngineID, b.Req)
		ref.Ingest(b.EngineID, b.Req)
	}
	want := InWindow(batches, base)
	h.Visible(tb, want)
	from, to := Window(base)
	ctx := context.Background()
	name := func(prefix string) string { return prefix + "-" + run + ".test." }
	all := querylog.Query{From: from, To: to, Name: run}
	with := func(mutate func(q *querylog.Query)) querylog.Query {
		q := all
		mutate(&q)
		return q
	}
	search := func(tb testing.TB, q querylog.Query) []querylog.Record {
		tb.Helper()
		return compareSearch(tb, h.Backend, ref, q)
	}

	sub(tb, "partial-name", func(tb testing.TB) {
		for _, tc := range []struct {
			name string
			want int
		}{
			{"you-" + run, 2},
			{run + ".TUBE.TEST.", 1},
			{"x*y-" + run, 1},
			{"x?y-" + run, 1}, // X?Y literal, case-insensitive
			{"x_y_" + run, 0}, // _ is literal: as a LIKE wildcard it would match x*y- and x%y_
			{"x%y_" + run, 1}, // % is literal: as a LIKE wildcard it would also match x*y-
			{"absent-" + run, 0},
		} {
			if got := search(tb, with(func(q *querylog.Query) { q.Name = tc.name })); len(got) != tc.want {
				tb.Errorf("name %q: %d records %v, want %d", tc.name, len(got), names(got), tc.want)
			}
		}
	})

	sub(tb, "multi-value", func(tb testing.TB) {
		got := search(tb, with(func(q *querylog.Query) { q.QTypes = []string{"A", "AAAA"} }))
		if len(got) != want-1 || !slices.ContainsFunc(got, func(r querylog.Record) bool { return r.QType == "AAAA" }) ||
			slices.ContainsFunc(got, func(r querylog.Record) bool { return r.QType != "A" && r.QType != "AAAA" }) {
			tb.Errorf("qtypes A, AAAA: %d records %v, want %d of A or AAAA only", len(got), names(got), want-1)
		}
		got = search(tb, with(func(q *querylog.Query) { q.RCodes, q.QTypes = []string{"NXDOMAIN"}, []string{"A"} }))
		if !sameNames(got, name("you"), name("rpz")) {
			tb.Errorf("rcode NXDOMAIN and qtype A: %v", names(got))
		}
		if got = search(tb, with(func(q *querylog.Query) { q.Caches = []string{"hit"} })); !sameNames(got, name("you")) {
			tb.Errorf("cache hit: %v", names(got))
		}
		if got = search(tb, with(func(q *querylog.Query) { q.Client = "10.0.0.2" })); !sameNames(got, name("you")) {
			tb.Errorf("client 10.0.0.2: %v", names(got))
		}
		got = search(tb, with(func(q *querylog.Query) { q.Categories = []string{"gambling", "ads-tracking"} }))
		if len(got) != 4 {
			tb.Errorf("categories gambling, ads-tracking: %v, want casino and three top-b", names(got))
		}
		got = search(tb, with(func(q *querylog.Query) { q.ListIDs = []string{"l-custom", "allow-g1"} }))
		if !sameNames(got, name("ads"), "www.you-"+run+".tube.test.") {
			tb.Errorf("list ids: %v", names(got))
		}
		got = search(tb, with(func(q *querylog.Query) { q.EngineIDs = []string{"e2"} }))
		if len(got) == 0 || slices.ContainsFunc(got, func(r querylog.Record) bool { return r.EngineID != "e2" }) {
			tb.Errorf("engine e2: %d records with engines %v", len(got), got)
		}
	})

	sub(tb, "reason-fields", func(tb testing.TB) {
		one := func(q querylog.Query, what string) querylog.Record {
			tb.Helper()
			got := search(tb, q)
			if len(got) != 1 {
				tb.Fatalf("%s: %d records %v, want 1", what, len(got), names(got))
			}
			return got[0]
		}
		r := one(with(func(q *querylog.Query) { q.Sources = []string{"allowlist"} }), "source allowlist")
		if r.Rule != "tube.test" || r.PolicyGroupID != "g1" || r.ListID != "allow-g1" || r.Filter != "allowed" {
			tb.Errorf("allowlist record: %+v", r)
		}
		r = one(with(func(q *querylog.Query) { q.Sources = []string{"rpz"} }), "source rpz")
		if r.RPZZoneID != "z1" || r.RPZAction != "nxdomain" || r.RCode != "NXDOMAIN" {
			tb.Errorf("rpz record: %+v", r)
		}
		r = one(with(func(q *querylog.Query) { q.Sources = []string{"acl"} }), "source acl")
		if r.ACLRefused != "authoritative" || r.RCode != "REFUSED" {
			tb.Errorf("acl record: %+v", r)
		}
		r = one(with(func(q *querylog.Query) { q.Sources = []string{"rewrite"} }), "source rewrite")
		if r.Rule != "rw-"+run+".test" || r.EngineID != "e2" {
			tb.Errorf("rewrite record: %+v", r)
		}
		r = one(with(func(q *querylog.Query) { q.Name = "race-" + run }), "race")
		if r.UpstreamsRaced != 3 || r.DurationUS != 1234 || r.Transport != "tcp" || r.Upstream != "fx" {
			tb.Errorf("race record: %+v", r)
		}
		r = one(with(func(q *querylog.Query) { q.Name = "edge-from-" + run }), "default record")
		if r.Client != "10.0.0.1" || r.QType != "A" || r.RCode != "NOERROR" || r.Cache != "miss" || r.Filter != "none" ||
			r.Transport != "udp" || r.DurationUS != 100 || r.RPZAction != "" || r.PolicyGroupID != "" || r.EngineID != "e1" ||
			r.UpstreamsRaced != 0 || !r.Time.Equal(from) {
			tb.Errorf("default record: %+v", r)
		}
	})

	sub(tb, "policy-group-global", func(tb testing.TB) {
		got := search(tb, with(func(q *querylog.Query) { q.PolicyGroups = []string{querylog.GlobalPolicyGroup} }))
		if len(got) != want-2 || slices.ContainsFunc(got, func(r querylog.Record) bool { return r.PolicyGroupID != "" }) {
			tb.Errorf("global: %d records %v, want %d without a policy group", len(got), names(got), want-2)
		}
		if got = search(tb, with(func(q *querylog.Query) { q.PolicyGroups = []string{querylog.GlobalPolicyGroup, "g1"} })); len(got) != want {
			tb.Errorf("global and g1: %d records, want %d", len(got), want)
		}
	})

	sub(tb, "filter-generations", func(tb testing.TB) {
		got := search(tb, with(func(q *querylog.Query) { q.Filters = []string{"blocked"} }))
		if len(got) != 6 || !slices.Contains(names(got), name("renamed")) {
			tb.Errorf("blocked: %v, want 6 including %s", names(got), name("renamed"))
		}
	})

	sub(tb, "time-range-inclusive", func(tb testing.TB) {
		wide := search(tb, with(func(q *querylog.Query) { q.From, q.To = from.Add(-time.Second), to.Add(time.Second) }))
		if n := names(wide); len(wide) != want+2 || !slices.Contains(n, name("before")) || !slices.Contains(n, name("after")) {
			tb.Fatalf("wider window: %v, want %d including before and after", n, want+2)
		}
		n := names(search(tb, all))
		if len(n) != want || !slices.Contains(n, name("edge-from")) || !slices.Contains(n, name("edge-to")) ||
			slices.Contains(n, name("before")) || slices.Contains(n, name("after")) {
			tb.Errorf("window: %v, want %d including both edges and neither outside record", n, want)
		}
	})

	sub(tb, "paging-no-gaps-no-duplicates", func(tb testing.TB) {
		if got := search(tb, all); len(got) != want {
			tb.Errorf("all: %d records, want %d", len(got), want)
		}
		ties := 0
		for _, r := range collect(tb, h.Backend, all, 1) {
			if r.Name == name("tie") {
				ties++
			}
		}
		if ties != 3 {
			tb.Errorf("limit 1: %d tie records, want 3", ties)
		}
	})

	sub(tb, "invalid-cursor", func(tb testing.TB) {
		if _, err := h.Backend.Search(ctx, with(func(q *querylog.Query) { q.Cursor = "not-a-cursor" })); !errors.Is(err, querylog.ErrInvalidCursor) {
			tb.Errorf("cursor not-a-cursor: %v, want ErrInvalidCursor", err)
		}
	})

	topper, ok := h.Backend.(querylog.Topper)
	if !ok {
		tb.Fatalf("backend %s does not implement querylog.Topper", h.Backend.Name())
	}
	// top compares the backend's top list with the reference's; onlyRun keeps the keys of this run
	// so a backend holding other records still compares.
	top := func(tb testing.TB, q querylog.TopQuery, onlyRun bool) []querylog.TopEntry {
		tb.Helper()
		got, err := topper.Top(ctx, q)
		if err != nil {
			tb.Fatalf("top %+v: %v", q, err)
		}
		exp, err := ref.Top(ctx, q)
		if err != nil {
			tb.Fatalf("reference top %+v: %v", q, err)
		}
		if onlyRun {
			got, exp = keysOf(got, run), keysOf(exp, run)
		}
		if !slices.Equal(got, exp) {
			tb.Errorf("top %+v: %v, reference %v", q, got, exp)
		}
		return got
	}
	expect := func(tb testing.TB, what string, got []querylog.TopEntry, want ...querylog.TopEntry) {
		tb.Helper()
		if !slices.Equal(got, want) {
			tb.Errorf("%s: %v, want %v", what, got, want)
		}
	}
	entry := func(key string, n int64) querylog.TopEntry { return querylog.TopEntry{Key: key, Count: n} }

	sub(tb, "top-name", func(tb testing.TB) {
		got := top(tb, querylog.TopQuery{From: from, To: to, Field: querylog.TopName, Limit: 4}, true)
		expect(tb, "top names", got, entry(name("top-a"), 4), entry(name("tie"), 3), entry(name("top-b"), 3), entry(name("top-c"), 3))
	})
	sub(tb, "top-ties-key-ascending", func(tb testing.TB) {
		got := top(tb, querylog.TopQuery{From: from, To: to, Field: querylog.TopName, Limit: 2}, true)
		expect(tb, "top 2 names", got, entry(name("top-a"), 4), entry(name("tie"), 3))
	})
	sub(tb, "top-blocked", func(tb testing.TB) {
		got := top(tb, querylog.TopQuery{From: from, To: to, Field: querylog.TopName, Filters: []string{"blocked"}, Limit: 10}, true)
		expect(tb, "top blocked names", got, entry(name("top-b"), 3), entry(name("ads"), 1), entry(name("casino"), 1), entry(name("renamed"), 1))
	})
	sub(tb, "top-client", func(tb testing.TB) {
		got := top(tb, querylog.TopQuery{From: from, To: to, Field: querylog.TopClient, Limit: 1}, false)
		expect(tb, "top client", got, entry("10.0.0.1", 13))
	})
	sub(tb, "top-category-skips-empty", func(tb testing.TB) {
		got := top(tb, querylog.TopQuery{From: from, To: to, Field: querylog.TopCategory, Limit: 10}, false)
		expect(tb, "top categories", got, entry("ads-tracking", 3), entry("gambling", 1))
	})
	sub(tb, "top-time-range-inclusive", func(tb testing.TB) {
		has := func(got []querylog.TopEntry, key string) bool {
			return slices.ContainsFunc(got, func(e querylog.TopEntry) bool { return e.Key == key })
		}
		wide := top(tb, querylog.TopQuery{From: from.Add(-time.Second), To: to.Add(time.Second), Field: querylog.TopName, Limit: 1000}, true)
		if !has(wide, name("before")) || !has(wide, name("after")) {
			tb.Fatalf("wider window top: %v, want before and after", wide)
		}
		got := top(tb, querylog.TopQuery{From: from, To: to, Field: querylog.TopName, Limit: 1000}, true)
		if !has(got, name("edge-from")) || !has(got, name("edge-to")) || has(got, name("before")) || has(got, name("after")) {
			tb.Errorf("window top: %v, want both edges and neither outside record", got)
		}
	})
}

// sub runs fn as a subtest when tb is a *testing.T, and directly otherwise so a recording TB sees
// every failure.
func sub(tb testing.TB, name string, fn func(testing.TB)) {
	if t, ok := tb.(*testing.T); ok {
		t.Run(name, func(t *testing.T) { fn(t) })
		return
	}
	fn(tb)
}

// compareSearch checks that q returns the reference's records from b at every page limit and
// returns the reference records.
func compareSearch(tb testing.TB, b querylog.Backend, ref *querylog.Builtin, q querylog.Query) []querylog.Record {
	tb.Helper()
	q.Limit, q.Cursor = 1000, ""
	p, err := ref.Search(context.Background(), q)
	if err != nil || p.NextCursor != "" {
		tb.Fatalf("reference search %+v: %v (cursor %q)", q, err, p.NextCursor)
	}
	want := p.Records
	for _, limit := range pageLimits {
		got := collect(tb, b, q, limit)
		if d := diff(want, got); d != "" {
			tb.Errorf("search %+v at limit %d: %s", q, limit, d)
		}
	}
	return want
}

// collect follows the cursors of q at limit and returns the concatenated pages.
func collect(tb testing.TB, b querylog.Backend, q querylog.Query, limit int) []querylog.Record {
	tb.Helper()
	q.Limit, q.Cursor = limit, ""
	var out []querylog.Record
	seen := map[string]bool{}
	for range maxPages {
		p, err := b.Search(context.Background(), q)
		if err != nil {
			tb.Fatalf("search %+v: %v", q, err)
		}
		if len(p.Records) > limit {
			tb.Errorf("search %+v: page of %d records exceeds the limit", q, len(p.Records))
		}
		for _, r := range p.Records {
			k := recordKey(r)
			if seen[k] {
				tb.Errorf("search %+v at limit %d: record repeated across pages: %+v", q, limit, r)
			}
			seen[k] = true
			out = append(out, r)
		}
		if p.NextCursor == "" {
			return out
		}
		q.Cursor = p.NextCursor
	}
	tb.Fatalf("search %+v at limit %d: more than %d pages", q, limit, maxPages)
	return nil
}

// diff compares two result lists as a sequence of millisecond timestamps and a multiset of
// records; it returns "" when they match.
func diff(want, got []querylog.Record) string {
	ms := func(rs []querylog.Record) []int64 {
		out := make([]int64, len(rs))
		for i, r := range rs {
			out[i] = r.Time.UnixMilli()
		}
		return out
	}
	if !slices.Equal(ms(want), ms(got)) {
		return fmt.Sprintf("millisecond sequence %v (%v), reference %v (%v)", ms(got), names(got), ms(want), names(want))
	}
	keys := func(rs []querylog.Record) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = recordKey(r)
		}
		slices.Sort(out)
		return out
	}
	if w, g := keys(want), keys(got); !slices.Equal(w, g) {
		return fmt.Sprintf("records differ:\n got %q\nwant %q", g, w)
	}
	return ""
}

// recordKey joins every Record field except Time, plus the millisecond time, with \x1f.
func recordKey(r querylog.Record) string {
	v := reflect.ValueOf(r)
	parts := []string{fmt.Sprint(r.Time.UnixMilli())}
	for i := range v.NumField() {
		if v.Type().Field(i).Name != "Time" {
			parts = append(parts, fmt.Sprint(v.Field(i).Interface()))
		}
	}
	return strings.Join(parts, "\x1f")
}

func names(rs []querylog.Record) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

// sameNames reports whether rs holds exactly the given names, in any order.
func sameNames(rs []querylog.Record, want ...string) bool {
	got := names(rs)
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	return slices.Equal(got, want)
}

// keysOf keeps the entries whose key contains run.
func keysOf(es []querylog.TopEntry, run string) []querylog.TopEntry {
	var out []querylog.TopEntry
	for _, e := range es {
		if strings.Contains(e.Key, run) {
			out = append(out, e)
		}
	}
	return out
}

// OTLPExport exports every batch in order over plain-text OTLP gRPC to grpcAddr.
func OTLPExport(t testing.TB, grpcAddr string, batches []EngineBatch) {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("otlp dial %s: %v", grpcAddr, err)
	}
	defer func() { _ = conn.Close() }()
	client := collogspb.NewLogsServiceClient(conn)
	for _, b := range batches {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := client.Export(ctx, b.Req)
		cancel()
		if err != nil {
			t.Fatalf("otlp export for engine %s: %v", b.EngineID, err)
		}
	}
}

// PollVisible returns a Visible function that polls b every 250 ms for up to 60 s until the window
// of base holds at least want records.
func PollVisible(b querylog.Backend, base time.Time) func(t testing.TB, want int) {
	return func(t testing.TB, want int) {
		t.Helper()
		from, to := Window(base)
		deadline := time.Now().Add(pollTimeout)
		n := 0
		var lastErr error
		for {
			p, err := b.Search(context.Background(), querylog.Query{From: from, To: to, Limit: 1000})
			n, lastErr = len(p.Records), err
			if err == nil && n >= want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d of %d records visible after %s (last error %v)", b.Name(), n, want, pollTimeout, lastErr)
			}
			time.Sleep(pollInterval)
		}
	}
}
