package blocklist

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
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
