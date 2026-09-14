package catalog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

var errUnchanged = errors.New("filter catalog unchanged")

var syncActor = auth.Actor{Type: "system", ID: "filter-catalog", Name: "system"}

// Sync writes the catalog into filter_categories and one catalog-managed filter_lists row per source,
// and publishes a config version. It changes nothing when the digest of data was already synced.
// Operator state (category and source enabled flags) survives; rows of removed sources and categories
// are deleted.
func Sync(ctx context.Context, st *store.Store, build snapshot.BuildConfig, c *Catalog, data []byte) (bool, error) {
	digest := Digest(data)
	synced := func(q store.PolicyQuerier) (bool, error) {
		var current string
		if err := q.QueryRow(ctx, "select digest from filter_catalog_state").Scan(&current); err != nil {
			return false, err
		}
		return current == digest, nil
	}
	if same, err := synced(st.Pool); err != nil || same {
		return false, err
	}
	_, err := snapshot.Mutate(ctx, st, build, syncActor, func(tx pgx.Tx) (auth.Change, error) {
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('nexora:catalog'))"); err != nil {
			return auth.Change{}, err
		}
		// Another instance may have synced while this one waited for the lock.
		if same, err := synced(tx); err != nil || same {
			if err == nil {
				err = errUnchanged
			}
			return auth.Change{}, err
		}
		var categories, pairs []string
		sources := 0
		for _, cat := range c.Categories {
			categories = append(categories, cat.Key)
			if _, err := tx.Exec(ctx, "insert into filter_categories(key) values ($1) on conflict (key) do nothing", cat.Key); err != nil {
				return auth.Change{}, err
			}
			for _, s := range cat.Sources {
				sources++
				pairs = append(pairs, cat.Key+":"+s.Key)
				// An update never touches enabled: the operator's toggle survives releases.
				if _, err := tx.Exec(ctx, `insert into filter_lists(name, kind, url, refresh_interval_seconds, enabled, category_key,
					source_key, managed_by_catalog, archive_member, catalog_position) values ($1, 'block', $2, $3, $4, $5, $6, true, $7, $8)
					on conflict (category_key, source_key) where managed_by_catalog do update set name = excluded.name, url = excluded.url,
					refresh_interval_seconds = excluded.refresh_interval_seconds, archive_member = excluded.archive_member,
					catalog_position = excluded.catalog_position`,
					ListName(cat.Key, s.Key), s.URL, s.RefreshIntervalSeconds, s.DefaultEnabled, cat.Key, s.Key, s.ArchiveMember,
					c.Position(cat.Key, s.Key)); err != nil {
					return auth.Change{}, err
				}
			}
		}
		if _, err := tx.Exec(ctx, "delete from filter_lists where managed_by_catalog and not (category_key || ':' || source_key = any($1))", pairs); err != nil {
			return auth.Change{}, err
		}
		if _, err := tx.Exec(ctx, "update policy_groups set category_keys = array(select k from unnest(category_keys) k where k = any($1))", categories); err != nil {
			return auth.Change{}, err
		}
		if _, err := tx.Exec(ctx, "delete from filter_categories where not (key = any($1))", categories); err != nil {
			return auth.Change{}, err
		}
		if _, err := tx.Exec(ctx, "update filter_catalog_state set digest = $1", digest); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "syncFilterCatalog", TargetType: "filter_catalog", TargetID: digest,
			After: map[string]any{"categories": len(categories), "sources": sources}}, nil
	})
	if errors.Is(err, errUnchanged) {
		return false, nil
	}
	return err == nil, err
}
