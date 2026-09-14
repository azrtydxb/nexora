package catalog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestSyncCreatesManagedListsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, c, catalog.Raw())
	if err != nil || !changed {
		t.Fatalf("first sync: changed=%v err=%v", changed, err)
	}
	cats, err := store.ListFilterCategories(ctx, st.Pool)
	if err != nil || len(cats) != len(c.Categories) {
		t.Fatalf("categories %d err %v", len(cats), err)
	}
	for _, cat := range cats {
		if cat.Enabled {
			t.Errorf("category %s enabled on a new install", cat.Key)
		}
	}
	lists, err := store.ListCatalogLists(ctx, st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, cat := range c.Categories {
		total += len(cat.Sources)
	}
	if len(lists) != total {
		t.Fatalf("managed lists %d, catalog sources %d", len(lists), total)
	}
	for _, l := range lists {
		s, _ := c.Source(l.CategoryKey, l.SourceKey)
		if l.Enabled != s.DefaultEnabled {
			t.Errorf("%s enabled=%v, default %v", l.SourceKey, l.Enabled, s.DefaultEnabled)
		}
	}
	var version int64
	_ = st.Pool.QueryRow(ctx, "select max(version) from config_versions").Scan(&version)
	if changed, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, c, catalog.Raw()); err != nil || changed {
		t.Fatalf("second sync with the same catalog: changed=%v err=%v", changed, err)
	}
	var after int64
	_ = st.Pool.QueryRow(ctx, "select max(version) from config_versions").Scan(&after)
	if after != version {
		t.Fatalf("an unchanged catalog published version %d (was %d)", after, version)
	}
	// An operator's source toggle survives a release that drops another source.
	if _, err := st.Pool.Exec(ctx, "update filter_lists set enabled = false where source_key = 'hagezi-gambling'"); err != nil {
		t.Fatal(err)
	}
	trimmed := strings.Replace(string(catalog.Raw()), "key: blp-fortnite", "key: blp-fortnite-removed-in-test", 1)
	next, err := catalog.Parse([]byte(trimmed))
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, next, []byte(trimmed)); err != nil || !changed {
		t.Fatalf("changed catalog: changed=%v err=%v", changed, err)
	}
	var fortnite, renamed int
	var gambling bool
	_ = st.Pool.QueryRow(ctx, `select count(*) filter (where source_key = 'blp-fortnite'), count(*) filter (where source_key = 'blp-fortnite-removed-in-test'),
		bool_or(enabled) filter (where source_key = 'hagezi-gambling') from filter_lists where managed_by_catalog`).Scan(&fortnite, &renamed, &gambling)
	if fortnite != 0 || renamed != 1 || gambling {
		t.Fatalf("after sync: blp-fortnite=%d renamed=%d hagezi-gambling enabled=%v", fortnite, renamed, gambling)
	}
}
