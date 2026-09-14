package snapshot_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/jackc/pgx/v5"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestCategoryListsInSnapshots(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, cat, catalog.Raw()); err != nil {
		t.Fatal(err)
	}
	var custom string
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		for i, src := range []string{"hagezi-gambling", "blp-gambling", "stevenblack-gambling", "hagezi-nsfw", "ut1-adult"} {
			sha, _, err := store.PutBlob(ctx, tx, []byte{byte(i), 1, 2})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "update filter_lists set current_blob_sha256 = $1 where source_key = $2", sha, src); err != nil {
				return err
			}
		}
		sha, _, err := store.PutBlob(ctx, tx, []byte("custom"))
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `insert into filter_lists(name, kind, url, current_blob_sha256) values ('my-list', 'block', 'https://x.test/a', $1) returning id::text`, sha).Scan(&custom); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update filter_categories set enabled = true where key = 'gambling'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update filter_lists set enabled = false where source_key = 'blp-gambling'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update engine_groups set filter_index_max_bytes = 67108864 where id = $1", store.DefaultEngineGroupID); err != nil {
			return err
		}
		_, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "kids", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.7.0.0/16")}, CategoryKeys: []string{"adult"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var snap *controlv1.ConfigSnapshot
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		snap, err = snapshot.BuildForGroup(ctx, tx, 99, snapshot.BuildConfig{}, store.DefaultEngineGroupID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	refs := snap.Filter.BlocklistRefs
	got := map[string]*controlv1.FilterListRef{}
	for _, r := range refs {
		got[r.Blob.Name] = r
	}
	if len(refs) != 3 || got["catalog:gambling:hagezi-gambling"] == nil || got["catalog:gambling:stevenblack-gambling"] == nil || got["my-list"] == nil {
		t.Fatalf("global refs %v: enabled gambling sources plus the custom list", refs)
	}
	if got["catalog:gambling:hagezi-gambling"].Category != "gambling" || got["my-list"].Category != "" || got["my-list"].ListId != custom {
		t.Fatalf("identity: %v", refs)
	}
	if refs[0].Position >= refs[1].Position || refs[2].Position < snapshot.CustomListPosition {
		t.Fatalf("catalog order first, custom lists after: %v", refs)
	}
	if len(snap.Filter.Blocklists) != len(refs) {
		t.Fatalf("M1 blocklists %d must mirror %d refs for older engines", len(snap.Filter.Blocklists), len(refs))
	}
	if len(snap.PolicyGroups) != 1 || len(snap.PolicyGroups[0].BlocklistRefs) != 1 || snap.PolicyGroups[0].BlocklistRefs[0].Blob.Name != "catalog:adult:hagezi-nsfw" {
		t.Fatalf("group refs %v: only the enabled adult source with a blob", snap.PolicyGroups)
	}
	if snap.FilterIndexMaxBytes != 67108864 {
		t.Fatalf("filter_index_max_bytes = %d", snap.FilterIndexMaxBytes)
	}
}
