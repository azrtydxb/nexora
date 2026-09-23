package catzone_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/catzone"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

func TestProducerRegeneratesInTheSameVersion(t *testing.T) {
	env := newCatEnv(t)
	cat := env.createCatalog(t, catzone.CreateInput{Name: "catalog.test.", Role: "producer",
		Transfer: zone.TransferInput{AllowCIDRs: []string{"127.0.0.1/32"}}})
	if cat.Role != "producer" || len(cat.Members) != 0 {
		t.Fatalf("create: %+v", cat)
	}
	v0 := env.latestVersion(t)
	a := env.createMember(t, "a.test.", cat.ID)
	if env.latestVersion(t) != v0+1 {
		t.Fatal("member creation and catalog regeneration must publish one version")
	}
	rrs := env.served(t, cat.ZoneID)
	members, err := catzone.Parse("catalog.test.", rrs)
	if err != nil || len(members) != 1 || members[0] != (catzone.Member{Label: catzone.Label(a), Zone: "a.test."}) {
		t.Fatalf("catalog after create: %v %v", members, err)
	}
	if q := env.allowQuery(t, cat.ZoneID); strings.Join(q, ",") != "127.0.0.1/32,::1/128" {
		t.Fatalf("catalog allow-query %v", q)
	}
	env.deleteMember(t, a)
	if members, _ := catzone.Parse("catalog.test.", env.served(t, cat.ZoneID)); len(members) != 0 {
		t.Fatalf("member not removed: %v", members)
	}
}

func TestConsumerReconcile(t *testing.T) {
	env := newCatEnv(t)
	cons := env.createCatalog(t, catzone.CreateInput{Name: "cat.remote.", Role: "consumer",
		Primaries: []zone.Endpoint{{Address: "127.0.0.1:5399", TSIGKeyID: env.tsigKey(t)}}})
	env.operatorZone(t, "c.remote.")
	env.loadCatalog(t, cons, 1, `version 0 IN TXT "2"
la.zones 0 IN PTR a.remote.
lb.zones 0 IN PTR b.remote.
lc.zones 0 IN PTR c.remote.`) // writes zone_records and serial as a refresh would
	if err := env.svc.Reconcile(context.Background(), cons.ZoneID); err != nil {
		t.Fatal(err)
	}
	v := env.get(t, cons.ID)
	a, b := env.zoneByName(t, "a.remote."), env.zoneByName(t, "b.remote.")
	if a.Kind != "secondary" || a.CatalogMemberLabel != "la" || *a.CatalogZoneID != cons.ID || a.Primaries[0].TSIGKeyID == nil || b == nil {
		t.Fatalf("members not created: %+v %+v", a, b)
	}
	if !hasMember(v, "c.remote.", "clash") || env.zoneByName(t, "c.remote.").CatalogZoneID != nil {
		t.Fatalf("clash not recorded or operator zone touched: %+v", v.Members)
	}
	if !env.refreshRequested(t, a.ID) {
		t.Fatal("no refresh requested for a new member")
	}
	oldA := a.ID
	env.loadCatalog(t, cons, 2, `version 0 IN TXT "2"
la2.zones 0 IN PTR a.remote.`)
	must(t, env.svc.Reconcile(context.Background(), cons.ZoneID))
	if env.zoneByName(t, "b.remote.") != nil {
		t.Fatal("removed member b.remote. still exists")
	}
	if na := env.zoneByName(t, "a.remote."); na == nil || na.ID == oldA || na.CatalogMemberLabel != "la2" {
		t.Fatalf("label change did not recreate a.remote.: %+v", na)
	}
	env.loadCatalog(t, cons, 3, `version 0 IN TXT "1"`)
	must(t, env.svc.Reconcile(context.Background(), cons.ZoneID))
	if v := env.get(t, cons.ID); v.BrokenReason != `unsupported catalog version "1"` || env.zoneByName(t, "a.remote.") == nil {
		t.Fatalf("broken catalog changed members or kept no reason: %+v", v)
	}
	env.expire(t, cons.ZoneID)
	env.loadCatalog(t, cons, 4, `version 0 IN TXT "2"`)
	must(t, env.svc.Reconcile(context.Background(), cons.ZoneID))
	if env.zoneByName(t, "a.remote.") == nil {
		t.Fatal("an expired catalog was processed")
	}
	env.unexpire(t, cons.ZoneID)
	must(t, env.svc.Reconcile(context.Background(), cons.ZoneID))
	if env.zoneByName(t, "a.remote.") != nil || env.get(t, cons.ID).BrokenReason != "" {
		t.Fatal("processing did not resume")
	}
	must(t, env.svc.Delete(context.Background(), testActor, cons.ID))
	if env.auditCount(t, "reconcileCatalogZone") < 3 {
		t.Fatal("reconciliations not audited")
	}
}

var testActor = auth.Actor{Type: "user", ID: "t", Name: "t"}

// catEnv is a store with a zone service and the catalog service. zone.Service's CatalogChanged
// hook (Task 14) is not relied on: createMember and deleteMember run the zone change and
// Regenerate in one transaction, as the hook does.
type catEnv struct {
	st    *store.Store
	zones *zone.Service
	svc   *catzone.Service
}

func newCatEnv(t *testing.T) *catEnv {
	t.Helper()
	st := storetest.New(t)
	zs := &zone.Service{Store: st, Now: time.Now}
	return &catEnv{st: st, zones: zs, svc: &catzone.Service{Store: st, Zones: zs, Now: time.Now}}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func hasMember(v catzone.View, name, state string) bool {
	for _, m := range v.Members {
		if m.Name == name && m.State == state {
			return true
		}
	}
	return false
}

func (e *catEnv) createCatalog(t *testing.T, in catzone.CreateInput) catzone.View {
	t.Helper()
	v, err := e.svc.Create(context.Background(), testActor, in)
	must(t, err)
	return v
}

func (e *catEnv) get(t *testing.T, id uuid.UUID) catzone.View {
	t.Helper()
	v, err := e.svc.Get(context.Background(), id)
	must(t, err)
	return v
}

func (e *catEnv) latestVersion(t *testing.T) int64 {
	t.Helper()
	var v int64
	must(t, e.st.Pool.QueryRow(context.Background(), "SELECT coalesce(max(version), 0) FROM config_versions").Scan(&v))
	return v
}

func (e *catEnv) createMember(t *testing.T, name string, catalogID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	_, err := snapshot.Mutate(ctx, e.st, snapshot.BuildConfig{}, testActor, func(tx pgx.Tx) (auth.Change, error) {
		z, err := e.zones.CreateZoneInTx(ctx, tx, testActor, zone.CreateZoneInput{Name: name, Kind: "primary",
			SOA: zone.SOA{MName: "ns." + name, RName: "h." + name}, Nameservers: []string{"ns." + name}, CatalogZoneID: &catalogID})
		if err != nil {
			return auth.Change{}, err
		}
		id = z.ID
		return auth.Change{Action: "createZone", TargetType: "zone", TargetID: id.String()}, e.svc.Regenerate(ctx, tx, []uuid.UUID{catalogID})
	})
	must(t, err)
	return id
}

func (e *catEnv) deleteMember(t *testing.T, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	z, err := e.zones.GetZone(ctx, id)
	must(t, err)
	_, err = snapshot.Mutate(ctx, e.st, snapshot.BuildConfig{}, testActor, func(tx pgx.Tx) (auth.Change, error) {
		if err := e.zones.DeleteZoneInTx(ctx, tx, testActor, id); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "deleteZone", TargetType: "zone", TargetID: id.String()}, e.svc.Regenerate(ctx, tx, []uuid.UUID{*z.CatalogZoneID})
	})
	must(t, err)
}

func (e *catEnv) served(t *testing.T, zoneID uuid.UUID) []dns.RR {
	t.Helper()
	ctx := context.Background()
	z, err := e.zones.GetZone(ctx, zoneID)
	must(t, err)
	var rrs []dns.RR
	must(t, e.st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		rrs, err = zone.LoadServed(ctx, tx, z)
		return err
	}))
	return rrs
}

func (e *catEnv) allowQuery(t *testing.T, zoneID uuid.UUID) []string {
	t.Helper()
	z, err := e.zones.GetZone(context.Background(), zoneID)
	must(t, err)
	out := make([]string, 0, len(z.AllowQueryCIDRs))
	for _, p := range z.AllowQueryCIDRs {
		out = append(out, p.String())
	}
	return out
}

func (e *catEnv) tsigKey(t *testing.T) *uuid.UUID {
	t.Helper()
	keys := &tsigkey.Service{Store: e.st, Box: kekBox(t)}
	secret := base64.StdEncoding.EncodeToString([]byte("catzone-test-secret-0123456789abc"))
	key, err := keys.Create(context.Background(), testActor, "cat-xfr.", "hmac-sha256", secret)
	must(t, err)
	return &key.ID
}

func kekBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	must(t, err)
	p := filepath.Join(t.TempDir(), "kek")
	must(t, os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600))
	box, err := secrets.Open(secrets.Config{KEKFile: p})
	must(t, err)
	return box
}

func (e *catEnv) operatorZone(t *testing.T, name string) {
	t.Helper()
	_, err := e.zones.CreateZone(context.Background(), testActor, zone.CreateZoneInput{Name: name, Kind: "primary",
		SOA: zone.SOA{MName: "ns." + name, RName: "h." + name}, Nameservers: []string{"ns." + name}})
	must(t, err)
}

// loadCatalog stores records (relative to the catalog) and serial on the consumer catalog zone and
// marks it loaded, as a successful transfer does.
func (e *catEnv) loadCatalog(t *testing.T, cat catzone.View, serial uint32, records string) {
	t.Helper()
	ctx := context.Background()
	zp := dns.NewZoneParser(strings.NewReader(records), cat.Name, "")
	var rrs []dns.RR
	for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
		rrs = append(rrs, rr)
	}
	must(t, zp.Err())
	_, err := e.zones.Mutate(ctx, cat.ZoneID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		opts := zone.RebuildOptions{Serial: &serial}
		if err := zone.SetRecords(ctx, tx, z.ID, rrs); err != nil {
			return "", nil, nil, opts, err
		}
		_, err := tx.Exec(ctx, "UPDATE zones SET loaded = true WHERE id = $1", z.ID)
		return "refreshZone", nil, nil, opts, err
	}, testActor)
	must(t, err)
}

func (e *catEnv) zoneByName(t *testing.T, name string) *zone.Zone {
	t.Helper()
	z, err := e.zones.GetZoneByName(context.Background(), name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	must(t, err)
	return z
}

func (e *catEnv) refreshRequested(t *testing.T, zoneID uuid.UUID) bool {
	t.Helper()
	var due bool
	must(t, e.st.Pool.QueryRow(context.Background(), `SELECT refresh_trigger = 'create' AND refresh_requests > 0 AND next_refresh_at <= now()
		FROM zones WHERE id = $1`, zoneID).Scan(&due))
	return due
}

func (e *catEnv) setExpired(t *testing.T, zoneID uuid.UUID, expired bool) {
	t.Helper()
	_, err := e.st.Pool.Exec(context.Background(), "UPDATE zones SET expired = $2 WHERE id = $1", zoneID, expired)
	must(t, err)
}

func (e *catEnv) expire(t *testing.T, zoneID uuid.UUID)   { e.setExpired(t, zoneID, true) }
func (e *catEnv) unexpire(t *testing.T, zoneID uuid.UUID) { e.setExpired(t, zoneID, false) }

func (e *catEnv) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	must(t, e.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE action = $1", action).Scan(&n))
	return n
}
