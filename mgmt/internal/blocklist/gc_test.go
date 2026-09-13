package blocklist

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// An uploaded RPZ zone file is referenced by rpz_zones.blob_sha256: collection must keep it (and
// not fail on the foreign key) while still deleting unreferenced blobs.
func TestCollectBlobsKeepsRPZZoneFiles(t *testing.T) {
	ctx := context.Background()
	pg := harness.New(t).StartPostgres()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var rpzSHA, orphanSHA string
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		z, err := store.CreateRPZZone(ctx, tx, store.RPZZone{Name: "rpz.file.test.", SourceType: "file", MinRefreshSeconds: 60, PolicyOverride: "given"})
		if err != nil {
			return err
		}
		if rpzSHA, _, err = store.PutBlob(ctx, tx, []byte("rpz zone")); err != nil {
			return err
		}
		if _, err = store.SetRPZZoneFile(ctx, tx, z.ID, z.Revision, rpzSHA, 1); err != nil {
			return err
		}
		orphanSHA, _, err = store.PutBlob(ctx, tx, []byte("orphan"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f := NewFetcher(st, snapshot.BuildConfig{}, &http.Client{})
	if err := f.collectBlobs(ctx); err != nil {
		t.Fatalf("collectBlobs: %v", err)
	}
	for sha, want := range map[string]int{orphanSHA: 0, rpzSHA: 1} {
		var n int
		if err := st.Pool.QueryRow(ctx, "select count(*) from blobs where sha256 = $1", sha).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("blob %s: %d rows, want %d", sha, n, want)
		}
	}
}

// A group's stable snapshot stays runnable however many versions were published since: its blobs
// are kept, while a blob only an old superseded version referenced is collected.
func TestCollectBlobsKeepsStableGroupSnapshots(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	put := func(data string) string {
		var sha string
		if err := st.InTx(ctx, func(tx pgx.Tx) (err error) { sha, _, err = store.PutBlob(ctx, tx, []byte(data)); return err }); err != nil {
			t.Fatal(err)
		}
		return sha
	}
	stableSHA, oldSHA := put("stable list"), put("superseded list")
	withList := func(sha string) *controlv1.ConfigSnapshot {
		return &controlv1.ConfigSnapshot{Filter: &controlv1.FilterConfig{Blocklists: []*controlv1.BlobRef{{Sha256: sha, Name: "l"}}}}
	}
	stable, err := snapshot.PublishRaw(ctx, st, withList(stableSHA), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.PublishRaw(ctx, st, withList(oldSHA), "test"); err != nil {
		t.Fatal(err)
	}
	for range keptVersions + 1 {
		if _, err := snapshot.PublishRaw(ctx, st, &controlv1.ConfigSnapshot{}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Pool.Exec(ctx, "update engine_groups set stable_version = $1", int64(stable)); err != nil {
		t.Fatal(err)
	}
	if err := NewFetcher(st, snapshot.BuildConfig{}, &http.Client{}).collectBlobs(ctx); err != nil {
		t.Fatalf("collectBlobs: %v", err)
	}
	for sha, want := range map[string]int{oldSHA: 0, stableSHA: 1} {
		var n int
		if err := st.Pool.QueryRow(ctx, "select count(*) from blobs where sha256 = $1", sha).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("blob %s: %d rows, want %d", sha, n, want)
		}
	}
}
