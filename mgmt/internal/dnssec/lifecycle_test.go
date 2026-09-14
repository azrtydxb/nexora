package dnssec_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/dnssec"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

func tokenBox(t *testing.T) *secrets.Box {
	t.Helper()
	tok := harness.InitSoftHSM(t, "nexora-lifecycle")
	box, err := secrets.Open(secrets.Config{PKCS11Module: tok.Module, PKCS11TokenLabel: tok.Label, PKCS11PinFile: tok.PinFile,
		Installation: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = box.Close() })
	return box
}

func keyRefs(t *testing.T, st *store.Store, zoneID uuid.UUID) [][]byte {
	t.Helper()
	rows, err := st.Pool.Query(context.Background(), "SELECT key_ref FROM dnssec_keys WHERE zone_id = $1 AND state <> 'removed'", zoneID)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func inToken(box *secrets.Box, ref []byte) bool {
	_, _, err := box.PKCS11KeyAttributes(ref)
	return err == nil
}

// failCommit makes the surrounding transaction fail at COMMIT: a deferred foreign key is violated.
func failCommit(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `CREATE TEMP TABLE commit_failure_parent (id int PRIMARY KEY);
		CREATE TEMP TABLE commit_failure_child (id int REFERENCES commit_failure_parent DEFERRABLE INITIALLY DEFERRED);
		INSERT INTO commit_failure_child VALUES (1)`)
	return err
}

func TestDisableDestroysTokenKeysOnlyAfterCommit(t *testing.T) {
	ctx := context.Background()
	box := tokenBox(t)
	zs, z := signedZone(t, box, dnssec.Settings{NSECMode: "nsec", KeyBackend: secrets.BackendPKCS11})
	refs := keyRefs(t, zs.Store, z.ID)
	if len(refs) != 2 || !inToken(box, refs[0]) || !inToken(box, refs[1]) {
		t.Fatalf("signed zone's keys are not in the token: %d refs", len(refs))
	}

	_, err := zs.Mutate(ctx, z.ID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		if err := dnssec.Disable(ctx, tx, z.ID); err != nil {
			return "", nil, nil, zone.RebuildOptions{}, err
		}
		return "updateZoneDnssec", nil, nil, zone.RebuildOptions{Force: true}, failCommit(ctx, tx)
	}, actor)
	if err == nil {
		t.Fatal("the injected commit failure did not fail the transaction")
	}
	// Even a destroy run after the failed commit must find nothing queued.
	if err := dnssec.DestroyPending(ctx, zs.Store, box); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if !inToken(box, ref) {
			t.Fatal("a key was destroyed although the transaction removing it did not commit")
		}
	}
	if fresh, _ := zs.GetZone(ctx, z.ID); !fresh.DNSSECEnabled {
		t.Fatal("zone disabled by a failed transaction")
	}
	servedVerified(t, zs, z.ID, zs.Now())

	// A committed disable queues the keys; a destroy that cannot reach the token keeps them
	// queued, and a later run destroys them.
	svc := &dnssec.Service{Store: zs.Store, Box: box, Zones: zs}
	if _, err := zs.Mutate(ctx, z.ID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		return "updateZoneDnssec", nil, nil, zone.RebuildOptions{Force: true}, dnssec.Disable(ctx, tx, z.ID)
	}, actor); err != nil {
		t.Fatal(err)
	}
	if err := dnssec.DestroyPending(ctx, zs.Store, kekBox(t)); !errors.Is(err, secrets.ErrBackendUnavailable) {
		t.Fatalf("destroy without the token: %v", err)
	}
	var queued int
	if err := zs.Store.Pool.QueryRow(ctx, "SELECT count(*) FROM dnssec_key_destruction").Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("queued destructions after a failed run: %d %v", queued, err)
	}
	for _, ref := range refs {
		if !inToken(box, ref) {
			t.Fatal("key destroyed by a run that failed")
		}
	}
	if err := dnssec.DestroyPending(ctx, zs.Store, box); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if inToken(box, ref) {
			t.Fatal("committed disable left its key in the token")
		}
	}
	if err := zs.Store.Pool.QueryRow(ctx, "SELECT count(*) FROM dnssec_key_destruction").Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("queue not drained: %d %v", queued, err)
	}

	// The API path runs the destroy itself after its commit.
	cur, _ := zs.GetZone(ctx, z.ID)
	if _, err := svc.Update(ctx, actor, z.ID, cur.Revision, true, dnssec.Settings{NSECMode: "nsec", KeyBackend: secrets.BackendPKCS11}); err != nil {
		t.Fatal(err)
	}
	again := keyRefs(t, zs.Store, z.ID)
	cur, _ = zs.GetZone(ctx, z.ID)
	if _, err := svc.Update(ctx, actor, z.ID, cur.Revision, false, dnssec.Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range again {
		if inToken(box, ref) {
			t.Fatal("Service.Update disable left its key in the token")
		}
	}
}

// Catches: an orphan sweep that treats every Nexora-labelled token object as its own, so an
// installation sharing the token destroys another installation's live DNSSEC keys.
func TestSweepLeavesOtherInstallationsTokenKeys(t *testing.T) {
	ctx := context.Background()
	tok := harness.InitSoftHSM(t, "nexora-shared")
	open := func() *secrets.Box {
		box, err := secrets.Open(secrets.Config{PKCS11Module: tok.Module, PKCS11TokenLabel: tok.Label, PKCS11PinFile: tok.PinFile,
			Installation: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		return box
	}
	foreign := open()
	theirs, err := foreign.GenerateSigningKey(ctx, secrets.BackendPKCS11, dns.ECDSAP256SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.Close(); err != nil {
		t.Fatal(err)
	}
	box := open()
	t.Cleanup(func() { _ = box.Close() })
	if err := box.EnsureHSMWrapKey(ctx); err != nil {
		t.Fatal(err)
	}
	zs, _ := signedZone(t, box, dnssec.Settings{NSECMode: "nsec", KeyBackend: secrets.BackendPKCS11})
	mine, err := box.GenerateSigningKey(ctx, secrets.BackendPKCS11, dns.ECDSAP256SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !inToken(box, theirs.KeyRef) || !inToken(box, mine.KeyRef) {
		t.Fatal("generated keys are not in the token: the test would prove nothing")
	}
	n, err := dnssec.SweepTokenOrphans(ctx, zs.Store, box, 0)
	if err != nil || n != 1 {
		t.Fatalf("sweep destroyed %d keys (%v), want only this installation's unreferenced key", n, err)
	}
	if inToken(box, mine.KeyRef) {
		t.Fatal("this installation's orphaned key survived the sweep")
	}
	if !inToken(box, theirs.KeyRef) {
		t.Fatal("the sweep destroyed a key another installation created in the shared token")
	}
}

func TestSweepDestroysTokenKeysOfRolledBackTransactions(t *testing.T) {
	ctx := context.Background()
	box := tokenBox(t)
	if err := box.EnsureHSMWrapKey(ctx); err != nil {
		t.Fatal(err)
	}
	zs, kept := signedZone(t, box, dnssec.Settings{NSECMode: "nsec3", KeyBackend: secrets.BackendPKCS11})
	keptRefs := keyRefs(t, zs.Store, kept.ID)
	other, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "rolled.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.rolled.test.", RName: "h.rolled.test."}, Nameservers: []string{"ns1.rolled.test."}})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := zs.Store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := dnssec.Enable(ctx, tx, box, other.ID, dnssec.Settings{KeyBackend: secrets.BackendPKCS11}); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "SELECT key_ref FROM dnssec_keys WHERE zone_id = $1", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	orphans, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 2 || !inToken(box, orphans[0]) || !inToken(box, orphans[1]) {
		t.Fatalf("rolled-back keys are not in the token (%d): the test would prove nothing", len(orphans))
	}

	if n, err := dnssec.SweepTokenOrphans(ctx, zs.Store, box, dnssec.OrphanGrace); err != nil || n != 0 {
		t.Fatalf("sweep within the grace period destroyed %d keys: %v", n, err)
	}
	if !inToken(box, orphans[0]) {
		t.Fatal("orphan destroyed before its grace period ended")
	}
	n, err := dnssec.SweepTokenOrphans(ctx, zs.Store, box, 0)
	if err != nil || n != 2 {
		t.Fatalf("sweep destroyed %d keys: %v", n, err)
	}
	for _, ref := range orphans {
		if inToken(box, ref) {
			t.Fatal("orphaned key still in the token")
		}
	}
	for _, ref := range keptRefs {
		if !inToken(box, ref) {
			t.Fatal("sweep destroyed a key a committed row references")
		}
	}
	if _, _, err := box.PKCS11WrapKeyAttributes(); err != nil {
		t.Fatalf("sweep touched the token wrap key: %v", err)
	}
	for _, rr := range servedVerified(t, zs, kept.ID, zs.Now()) {
		if rr.Header().Rrtype == dns.TypeDNSKEY {
			return
		}
	}
	t.Fatal("kept zone serves no DNSKEY")
}
