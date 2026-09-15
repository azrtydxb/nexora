package filterrec_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/piwi3910/nexora/mgmt/internal/ai/filterrec"
	"github.com/piwi3910/nexora/mgmt/internal/blocklist"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func attr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// ingest adds n not-blocked records for name from client, one second apart, ending a minute before now.
func ingest(b *querylog.Builtin, now time.Time, client, name string, n int) {
	var recs []*logspb.LogRecord
	for i := range n {
		at := now.Add(-time.Minute - time.Duration(i)*time.Second)
		recs = append(recs, &logspb.LogRecord{TimeUnixNano: uint64(at.UnixNano()), Attributes: []*commonpb.KeyValue{
			attr("client.address", client), attr("dns.question.name", name+"."), attr("dns.question.type", "A"),
			attr("dns.response.code", "NOERROR"), attr("nexora.cache", "miss"), attr("nexora.filter", "none"), attr("nexora.engine.id", "e1"),
		}})
	}
	b.Ingest("e1", &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}}}}})
}

// seedGuest creates the policy group guest (10.9.0.0/24) and a disabled malware category whose one catalog
// list blob contains miner.aif.test, and returns the group.
func seedGuest(t *testing.T, st *store.Store) store.PolicyGroup {
	t.Helper()
	ctx := context.Background()
	data, _, err := blocklist.Compress(blocklist.Normalize([]string{"miner.aif.test", "other-miner.aif.test"}))
	if err != nil {
		t.Fatal(err)
	}
	var g store.PolicyGroup
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		sha, _, err := store.PutBlob(ctx, tx, data)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into filter_categories(key, enabled) values ('malware', false)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into filter_lists(name, kind, url, enabled, category_key, source_key, managed_by_catalog,
			catalog_position, current_blob_sha256) values ('malware: URLhaus', 'block', 'https://lists.aif.test/urlhaus', true, 'malware',
			'urlhaus', true, 1, $1)`, sha); err != nil {
			return err
		}
		g, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "guest", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.9.0.0/24")},
			SafeSearch: store.SafeSearch{YouTube: "off"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func loadCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestAggregateGroupStats(t *testing.T) {
	st := storetest.New(t)
	g := seedGuest(t, st)
	now := time.Now()
	b := querylog.NewBuiltin(1000)
	ingest(b, now, "10.9.0.5", "miner.aif.test", 30)
	ingest(b, now, "10.9.0.5", "www.youtube.com", 20)
	ingest(b, now, "10.9.0.5", "ok.aif.test", 50)

	stats, err := filterrec.Aggregate(context.Background(), st, b, loadCatalog(t), now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	var guest *filterrec.GroupStats
	for i := range stats {
		if stats[i].PolicyGroupID == g.ID.String() {
			guest = &stats[i]
		}
	}
	if guest == nil {
		t.Fatalf("no stats for guest: %+v", stats)
	}
	if guest.Total != 100 || guest.Blocked != 0 {
		t.Fatalf("total %d blocked %d, want 100 and 0", guest.Total, guest.Blocked)
	}
	if got := guest.DisabledCategoryHits["malware"]; got != 30 {
		t.Fatalf("malware hits %d, want 30 (%v)", got, guest.DisabledCategoryHits)
	}
	if got := guest.SearchHosts["youtube"]; got != 20 {
		t.Fatalf("youtube queries %d, want 20 (%v)", got, guest.SearchHosts)
	}
	if len(guest.Unblocked) != 3 || guest.Unblocked[0] != (filterrec.NameCount{Name: "ok.aif.test", Queries: 50, Clients: 1}) {
		t.Fatalf("unblocked %+v", guest.Unblocked)
	}
	if guest.SafeSearch.YouTube != "off" {
		t.Fatalf("safe search %+v", guest.SafeSearch)
	}
	for _, s := range stats {
		if s.PolicyGroupID == "" {
			t.Fatalf("records from 10.9.0.5 counted as global: %+v", s)
		}
	}
}
