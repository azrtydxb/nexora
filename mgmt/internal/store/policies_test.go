package store_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	pg := harness.New(t).StartPostgres()
	ctx := context.Background()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func inTx(t *testing.T, s *store.Store, fn func(tx pgx.Tx) error) error {
	t.Helper()
	return s.InTx(context.Background(), fn)
}

func TestPolicyGroupsRevisionAndCIDRUniqueness(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	var kids store.PolicyGroup
	err := inTx(t, s, func(tx pgx.Tx) error {
		var err error
		kids, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{
			Name:       "kids",
			CIDRs:      []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
			Allowlist:  []string{"school.example"},
			SafeSearch: store.SafeSearch{Google: true, YouTube: "strict"},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if kids.Revision != 1 || len(kids.CIDRs) != 1 {
		t.Fatalf("created = %+v", kids)
	}
	err = inTx(t, s, func(tx pgx.Tx) error {
		_, err := store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "narrow", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}})
		return err
	})
	if err != nil {
		t.Fatalf("overlapping narrower prefix must be allowed: %v", err)
	}
	err = inTx(t, s, func(tx pgx.Tx) error {
		_, err := store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "dup", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
		return err
	})
	if !errors.Is(err, store.ErrCIDRInUse) {
		t.Fatalf("duplicate prefix: got %v, want ErrCIDRInUse", err)
	}
	kids.Description = "first edit"
	err = inTx(t, s, func(tx pgx.Tx) error {
		updated, err := store.UpdatePolicyGroup(ctx, tx, kids)
		if err == nil && updated.Revision != 2 {
			t.Errorf("revision after update = %d", updated.Revision)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	kids.Description = "stale edit"
	err = inTx(t, s, func(tx pgx.Tx) error { _, err := store.UpdatePolicyGroup(ctx, tx, kids); return err })
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale revision: got %v, want ErrConflict", err)
	}
	got, err := store.GetPolicyGroup(ctx, s.Pool, kids.ID)
	if err != nil || got.Description != "first edit" || got.SafeSearch.YouTube != "strict" || got.Allowlist[0] != "school.example" {
		t.Fatalf("get = %+v, %v", got, err)
	}
}

func TestRewritesScopesAndGlobalSafeSearch(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	err := inTx(t, s, func(tx pgx.Tx) error {
		if _, err := store.CreateRewrite(ctx, tx, store.Rewrite{Name: "nas.home.test", Type: "A", Value: "192.168.1.50", TTL: 120}); err != nil {
			return err
		}
		_, err := store.CreateRewrite(ctx, tx, store.Rewrite{Name: "nas.home.test", Type: "CNAME", Value: "other.home.test", TTL: 120})
		return err
	})
	if !errors.Is(err, store.ErrRewriteCNAMEConflict) {
		t.Fatalf("CNAME beside A: got %v", err)
	}
	err = inTx(t, s, func(tx pgx.Tx) error {
		_, err := store.CreateRewrite(ctx, tx, store.Rewrite{Name: "*.lab.home.test", Type: "CNAME", Value: "nas.home.test", TTL: 60})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := store.ListRewrites(ctx, s.Pool, nil, true)
	if err != nil || len(all) != 1 || all[0].GroupID != nil {
		t.Fatalf("list = %+v, %v", all, err)
	}
	g, err := store.GetGlobalSafeSearch(ctx, s.Pool)
	if err != nil || g.Revision != 1 || g.YouTube != "off" {
		t.Fatalf("global = %+v, %v", g, err)
	}
	g.Bing = true
	err = inTx(t, s, func(tx pgx.Tx) error { _, err := store.UpdateGlobalSafeSearch(ctx, tx, g); return err })
	if err != nil {
		t.Fatal(err)
	}
	err = inTx(t, s, func(tx pgx.Tx) error { _, err := store.UpdateGlobalSafeSearch(ctx, tx, g); return err })
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale global safe search: got %v", err)
	}
}

func TestPolicyGroupCategoryKeysAndManagedListsExcluded(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	var managed uuid.UUID
	if _, err := s.Pool.Exec(ctx, "insert into filter_categories(key) values ('gambling')"); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `insert into filter_lists(name, kind, url, category_key, source_key, managed_by_catalog, catalog_position)
		values ('catalog:gambling:x', 'block', 'https://x.test/l.txt', 'gambling', 'x', true, 1) returning id`).Scan(&managed); err != nil {
		t.Fatal(err)
	}
	err := inTx(t, s, func(tx pgx.Tx) error {
		g, err := store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "kids", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.9.0.0/16")}, CategoryKeys: []string{"gambling"}})
		if err != nil || len(g.CategoryKeys) != 1 || g.CategoryKeys[0] != "gambling" {
			t.Fatalf("create with category keys: %+v %v", g, err)
		}
		_, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "kids2", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.8.0.0/16")}, FilterListIDs: []uuid.UUID{managed}})
		if !errors.Is(err, store.ErrUnknownFilterList) {
			t.Fatalf("a catalog-managed list selected by id -> %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
