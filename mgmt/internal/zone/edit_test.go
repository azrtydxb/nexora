package zone_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

// rowTracer records the most rows any zone_records SELECT returned.
type rowTracer struct {
	mu  sync.Mutex
	max int64
}

type rowTracerKey struct{}

func (r *rowTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, rowTracerKey{}, d.SQL)
}

func (r *rowTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(rowTracerKey{}).(string)
	if !strings.Contains(sql, "FROM zone_records") || !strings.HasPrefix(strings.TrimSpace(strings.ToUpper(sql)), "SELECT") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.max = max(r.max, d.CommandTag.RowsAffected())
}

func tracedService(t *testing.T) (*zone.Service, *rowTracer) {
	base := storetest.New(t)
	cfg, err := pgxpool.ParseConfig(base.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	tr := &rowTracer{}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &zone.Service{Store: &store.Store{Pool: pool}, Now: time.Now}, tr
}

// TestRecordEditDoesNotLoadWholeZone catches a create, update or delete of an unsigned zone
// record that reads the whole record set.
func TestRecordEditDoesNotLoadWholeZone(t *testing.T) {
	ctx := context.Background()
	s, tr := tracedService(t)
	z := createZone(t, s, "big.test.")
	if _, err := s.Store.Pool.Exec(ctx, `INSERT INTO zone_records (zone_id, owner, rtype, ttl, rdata, rdata_wire)
		SELECT $1, 'h' || i || '.big.test.', 1, 300, '192.0.2.1', '\xc0000201'::bytea FROM generate_series(0, 1999) i`, z.ID); err != nil {
		t.Fatal(err)
	}
	// Publish the bulk-inserted records and write an image, so no image falls due below.
	zone.RebuildWithImageForTest(t, s.Store.Pool, z.ID)
	tr.mu.Lock()
	tr.max = 0
	tr.mu.Unlock()
	r, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "new.big.test.", Type: "A", TTL: 300, Data: "192.0.2.2"})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.UpdateRecord(ctx, actor, z.ID, r.ID, r.Revision, zone.RecordInput{Name: "moved.big.test.", Type: "A", TTL: 300, Data: "192.0.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRecord(ctx, actor, z.ID, r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	if tr.max > 16 {
		t.Fatalf("a record edit read %d zone_records rows", tr.max)
	}
}

// TestIncrementalEditsMatchFullRebuild catches an incremental journal delta that lets the served
// zone drift from its stored records.
func TestIncrementalEditsMatchFullRebuild(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	z := createZone(t, s, "inc.test.")
	rng := rand.New(rand.NewPCG(1, 2))
	var live []*zone.Record
	edits := 0
	for i := range 200 {
		switch {
		case len(live) == 0 || rng.IntN(3) == 0:
			r, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: fmt.Sprintf("n%d.inc.test.", rng.IntN(40)), Type: "A", TTL: uint32(60 + rng.IntN(3)*60), Data: fmt.Sprintf("192.0.2.%d", i%250)})
			if err == nil {
				live = append(live, r)
				edits++
			}
		case rng.IntN(2) == 0:
			j := rng.IntN(len(live))
			r, err := s.UpdateRecord(ctx, actor, z.ID, live[j].ID, live[j].Revision, zone.RecordInput{Name: fmt.Sprintf("n%d.inc.test.", rng.IntN(40)), Type: "A", TTL: 300, Data: fmt.Sprintf("198.51.100.%d", i%250)})
			if err == nil {
				live[j] = r
				edits++
			}
		default:
			j := rng.IntN(len(live))
			if err := s.DeleteRecord(ctx, actor, z.ID, live[j].ID, live[j].Revision); err == nil {
				live = append(live[:j], live[j+1:]...)
				edits++
			}
		}
		// A sibling's TTL change bumps the revisions of the RRset: refresh them.
		for j := range live {
			if recs, _, err := s.ListRecords(ctx, z.ID, live[j].Name, live[j].Type, "", 1000); err == nil {
				for _, r := range recs {
					if r.ID == live[j].ID {
						live[j] = &r
					}
				}
			}
		}
	}
	z, err := s.GetZone(ctx, z.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	served, err := zone.LoadServed(ctx, tx, z)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := zone.LoadRecords(ctx, tx, z.ID)
	if err != nil {
		t.Fatal(err)
	}
	if edits < 100 {
		t.Fatalf("only %d of 200 random edits succeeded: the delta was not exercised", edits)
	}
	if diff := zone.DiffForTest(served, desired); diff != "" {
		t.Fatalf("served zone differs from its records after incremental edits:\n%s", diff)
	}
}

// TestOwnerScopedChecksKeepRules catches owner-scoped validation that lets dname_occludes,
// ds_not_at_delegation, cname_conflict or last_apex_ns through.
func TestOwnerScopedChecksKeepRules(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	z := createZone(t, s, "own.test.")
	must := func(in zone.RecordInput) *zone.Record {
		t.Helper()
		r, err := s.CreateRecord(ctx, actor, z.ID, in)
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		return r
	}
	code := func(err error, want string) {
		t.Helper()
		var ve *zone.ValidationError
		if !errors.As(err, &ve) || ve.Code != want {
			t.Fatalf("got %v, want %s", err, want)
		}
	}
	must(zone.RecordInput{Name: "x.sub.own.test.", Type: "A", TTL: 300, Data: "192.0.2.1"})
	_, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "sub.own.test.", Type: "DNAME", TTL: 300, Data: "other.test."})
	code(err, "dname_occludes")
	must(zone.RecordInput{Name: "d.own.test.", Type: "DNAME", TTL: 300, Data: "other.test."})
	_, err = s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "y.d.own.test.", Type: "A", TTL: 300, Data: "192.0.2.1"})
	code(err, "dname_occludes")
	_, err = s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "ds.own.test.", Type: "DS", TTL: 300, Data: "12345 13 2 " + strings.Repeat("ab", 32)})
	code(err, "ds_not_at_delegation")
	must(zone.RecordInput{Name: "host.own.test.", Type: "A", TTL: 300, Data: "192.0.2.9"})
	c := must(zone.RecordInput{Name: "alias.own.test.", Type: "CNAME", TTL: 300, Data: "www.example."})
	_, err = s.UpdateRecord(ctx, actor, z.ID, c.ID, c.Revision, zone.RecordInput{Name: "host.own.test.", Type: "CNAME", TTL: 300, Data: "www.example."})
	code(err, "cname_conflict")
	ns := apexNS(t, s, z)
	_, err = s.UpdateRecord(ctx, actor, z.ID, ns.ID, ns.Revision, zone.RecordInput{Name: "www.own.test.", Type: "NS", TTL: 300, Data: "ns1.own.test."})
	code(err, "last_apex_ns")
}

// apexNS returns the only apex NS record of z.
func apexNS(t *testing.T, s *zone.Service, z *zone.Zone) zone.Record {
	t.Helper()
	recs, _, err := s.ListRecords(context.Background(), z.ID, z.Name, "NS", "", 10)
	if err != nil || len(recs) != 1 {
		t.Fatalf("apex NS: %v (%d records)", err, len(recs))
	}
	return recs[0]
}
