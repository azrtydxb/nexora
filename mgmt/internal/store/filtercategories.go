package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FilterCategory is the operator state of one catalog category.
type FilterCategory struct {
	Key       string
	Enabled   bool
	Revision  int64
	UpdatedAt time.Time
}

// CatalogList is the filter_lists row that mirrors one catalog source.
type CatalogList struct {
	ID                     uuid.UUID
	CategoryKey, SourceKey string
	Enabled                bool
	EntryCount             int
	LastSuccessAt          *time.Time
	LastError              string
	Stale                  bool
	LicenseAcknowledgedAt  *time.Time
	CurrentBlobSHA256      *string
}

const selectFilterCategories = "select key, enabled, revision, updated_at from filter_categories"

func scanFilterCategory(row pgx.Row) (FilterCategory, error) {
	var c FilterCategory
	err := row.Scan(&c.Key, &c.Enabled, &c.Revision, &c.UpdatedAt)
	return c, err
}

// ListFilterCategories returns every category row ordered by key.
func ListFilterCategories(ctx context.Context, q PolicyQuerier) ([]FilterCategory, error) {
	rows, err := q.Query(ctx, selectFilterCategories+" order by key")
	if err != nil {
		return nil, MapError(err)
	}
	defer rows.Close()
	out := []FilterCategory{}
	for rows.Next() {
		c, err := scanFilterCategory(rows)
		if err != nil {
			return nil, MapError(err)
		}
		out = append(out, c)
	}
	return out, MapError(rows.Err())
}

// LockFilterCategory reads the category row for update; ErrNotFound when absent.
func LockFilterCategory(ctx context.Context, tx pgx.Tx, key string) (FilterCategory, error) {
	c, err := scanFilterCategory(tx.QueryRow(ctx, selectFilterCategories+" where key = $1 for update", key))
	return c, MapError(err)
}

// SetFilterCategoryEnabled stores the enabled flag and bumps the revision.
func SetFilterCategoryEnabled(ctx context.Context, tx pgx.Tx, key string, enabled bool) (FilterCategory, error) {
	c, err := scanFilterCategory(tx.QueryRow(ctx, `update filter_categories set enabled = $2, revision = revision + 1, updated_at = now()
		where key = $1 returning key, enabled, revision, updated_at`, key, enabled))
	return c, MapError(err)
}

// ListCatalogLists returns the catalog-managed filter lists in catalog order. A list is stale when
// its last refresh failed, it never succeeded, or it is older than two refresh intervals.
func ListCatalogLists(ctx context.Context, q PolicyQuerier) ([]CatalogList, error) {
	rows, err := q.Query(ctx, `select id, category_key, source_key, enabled, entry_count, last_success_at, last_error,
		(last_error <> '' or last_success_at is null or last_success_at < now() - 2 * refresh_interval_seconds * interval '1 second'),
		license_acknowledged_at, current_blob_sha256
		from filter_lists where managed_by_catalog order by catalog_position`)
	if err != nil {
		return nil, MapError(err)
	}
	defer rows.Close()
	out := []CatalogList{}
	for rows.Next() {
		var l CatalogList
		if err := rows.Scan(&l.ID, &l.CategoryKey, &l.SourceKey, &l.Enabled, &l.EntryCount, &l.LastSuccessAt, &l.LastError,
			&l.Stale, &l.LicenseAcknowledgedAt, &l.CurrentBlobSHA256); err != nil {
			return nil, MapError(err)
		}
		out = append(out, l)
	}
	return out, MapError(rows.Err())
}

// SetCatalogListEnabled toggles a catalog-managed list; acknowledged records the license
// acknowledgement time.
func SetCatalogListEnabled(ctx context.Context, tx pgx.Tx, id uuid.UUID, enabled, acknowledged bool) error {
	tag, err := tx.Exec(ctx, `update filter_lists set enabled = $2,
		license_acknowledged_at = case when $3 then now() else license_acknowledged_at end,
		revision = revision + 1, updated_at = now() where id = $1 and managed_by_catalog`, id, enabled, acknowledged)
	if err != nil {
		return MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
