package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// The filter category catalog is read-only: these handlers toggle categories and their sources and
// never add, edit or remove a source.

// catalogState is the stored operator state of the catalog: category rows by key and catalog-managed
// lists by "<category>:<source>".
type catalogState struct {
	categories map[string]store.FilterCategory
	lists      map[string]store.CatalogList
}

func loadCatalogState(ctx context.Context, q store.PolicyQuerier) (catalogState, error) {
	cats, err := store.ListFilterCategories(ctx, q)
	if err != nil {
		return catalogState{}, err
	}
	lists, err := store.ListCatalogLists(ctx, q)
	if err != nil {
		return catalogState{}, err
	}
	st := catalogState{categories: map[string]store.FilterCategory{}, lists: map[string]store.CatalogList{}}
	for _, c := range cats {
		st.categories[c.Key] = c
	}
	for _, l := range lists {
		st.lists[l.CategoryKey+":"+l.SourceKey] = l
	}
	return st, nil
}

func (s catalogState) list(category, source string) (store.CatalogList, error) {
	l, ok := s.lists[category+":"+source]
	if !ok {
		return l, fmt.Errorf("filter catalog not synced: source %s of category %s has no filter list", source, category)
	}
	return l, nil
}

func (s catalogState) categoryOut(cat catalog.Category) (FilterCategory, error) {
	row, ok := s.categories[cat.Key]
	if !ok {
		return FilterCategory{}, fmt.Errorf("filter catalog not synced: category %s has no row", cat.Key)
	}
	out := FilterCategory{Key: cat.Key, Name: cat.Name, Description: cat.Description, Enabled: row.Enabled, Revision: row.Revision,
		Sources: make([]FilterCategorySource, 0, len(cat.Sources))}
	for _, src := range cat.Sources {
		l, err := s.list(cat.Key, src.Key)
		if err != nil {
			return FilterCategory{}, err
		}
		out.Sources = append(out.Sources, FilterCategorySource{Key: src.Key, Name: src.Name, Url: src.URL,
			Format: FilterCategorySourceFormat(src.Format), ArchiveMember: src.ArchiveMember, License: src.License,
			LicenseUrl: src.LicenseURL, Attribution: src.Attribution, CommercialUse: src.CommercialUse, Notice: src.Notice,
			Enabled: l.Enabled, ListId: l.ID, EntryCount: l.EntryCount, LastSuccessAt: l.LastSuccessAt, LastError: l.LastError, Stale: l.Stale})
		out.Stale = out.Stale || (row.Enabled && l.Enabled && l.Stale)
	}
	return out, nil
}

func (h *handlers) ListFilterCategories(ctx context.Context, _ ListFilterCategoriesRequestObject) (ListFilterCategoriesResponseObject, error) {
	st, err := loadCatalogState(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	out := make(ListFilterCategories200JSONResponse, 0, len(h.d.Catalog.Categories))
	for _, cat := range h.d.Catalog.Categories {
		c, err := st.categoryOut(cat)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// licenseRequired refuses enabling sources that are not free for commercial use without
// acknowledge_license; details carry each source's notice.
func licenseRequired(sources []catalog.Source) error {
	keys := make([]string, 0, len(sources))
	details := make([]string, 0, len(sources))
	for _, s := range sources {
		keys = append(keys, s.Key)
		details = append(details, s.Key+": "+s.Notice)
	}
	return apiError{status: http.StatusUnprocessableEntity, code: "license_acknowledgement_required",
		msg:     fmt.Sprintf("sources %s are not free for commercial use; resend with acknowledge_license: true", strings.Join(keys, ", ")),
		details: details}
}

// categoryInGroups reports whether any policy group selects category key.
func categoryInGroups(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	var used bool
	err := tx.QueryRow(ctx, "select exists(select 1 from policy_groups where $1 = any(category_keys))", key).Scan(&used)
	return used, err
}

func (h *handlers) UpdateFilterCategory(ctx context.Context, req UpdateFilterCategoryRequestObject) (UpdateFilterCategoryResponseObject, error) {
	key, body := req.Key, req.Body
	cat, ok := h.d.Catalog.Category(key)
	if !ok {
		return nil, store.ErrNotFound
	}
	toggles := map[string]bool{}
	if body.Sources != nil {
		for _, s := range *body.Sources {
			if _, ok := h.d.Catalog.Source(key, s.Key); !ok {
				return nil, coded(http.StatusUnprocessableEntity, "unknown_source", "category %s has no source %s; catalog sources cannot be added", key, s.Key)
			}
			toggles[s.Key] = s.Enabled
		}
	}
	acknowledge := body.AcknowledgeLicense != nil && *body.AcknowledgeLicense
	var out FilterCategory
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		row, err := store.LockFilterCategory(ctx, tx, key)
		if err != nil {
			return auth.Change{}, err
		}
		if err := checkRevision(row.Revision, body.Revision); err != nil {
			return auth.Change{}, err
		}
		st, err := loadCatalogState(ctx, tx)
		if err != nil {
			return auth.Change{}, err
		}
		inGroups, err := categoryInGroups(ctx, tx, key)
		if err != nil {
			return auth.Change{}, err
		}
		before, after := map[string]bool{}, map[string]bool{}
		var needAck []catalog.Source
		for _, src := range cat.Sources {
			l, err := st.list(key, src.Key)
			if err != nil {
				return auth.Change{}, err
			}
			enabled := l.Enabled
			if v, ok := toggles[src.Key]; ok {
				enabled = v
			}
			before[src.Key], after[src.Key] = l.Enabled, enabled
			// A source is in effect while it is enabled and its category is enabled globally or selected by a
			// policy group; one that comes into effect and is not free for commercial use needs acknowledgement.
			wasActive := l.Enabled && (row.Enabled || inGroups)
			isActive := enabled && (body.Enabled || inGroups)
			if isActive && !wasActive && !src.CommercialUse {
				needAck = append(needAck, src)
			}
		}
		if len(needAck) > 0 && !acknowledge {
			return auth.Change{}, licenseRequired(needAck)
		}
		acknowledged := []string{}
		for _, src := range cat.Sources {
			l, _ := st.list(key, src.Key)
			ack := slices.ContainsFunc(needAck, func(s catalog.Source) bool { return s.Key == src.Key })
			if ack {
				acknowledged = append(acknowledged, src.Key)
			}
			if after[src.Key] != l.Enabled || ack {
				if err := store.SetCatalogListEnabled(ctx, tx, l.ID, after[src.Key], ack); err != nil {
					return auth.Change{}, err
				}
			}
		}
		if _, err := store.SetFilterCategoryEnabled(ctx, tx, key, body.Enabled); err != nil {
			return auth.Change{}, err
		}
		if st, err = loadCatalogState(ctx, tx); err != nil {
			return auth.Change{}, err
		}
		if out, err = st.categoryOut(cat); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "updateFilterCategory", TargetType: "filter_category", TargetID: key,
			Before: map[string]any{"enabled": row.Enabled, "sources": before},
			After:  map[string]any{"enabled": body.Enabled, "sources": after, "acknowledged_licenses": acknowledged}}, nil
	})
	if err != nil {
		return nil, err
	}
	return UpdateFilterCategory200JSONResponse(out), nil
}

// checkGroupCategories validates a policy group's category keys. Categories the group newly selects
// (not in before) whose enabled sources are not free for commercial use need acknowledged; the
// acknowledgement is recorded on those lists and their source keys are returned for the audit entry.
func (h *handlers) checkGroupCategories(ctx context.Context, tx pgx.Tx, before []string, g store.PolicyGroup, acknowledged bool) ([]string, error) {
	var added []catalog.Category
	for _, k := range g.CategoryKeys {
		cat, ok := h.d.Catalog.Category(k)
		if !ok {
			return nil, coded(http.StatusUnprocessableEntity, "unknown_category", "category_keys: unknown category %s", k)
		}
		if !slices.Contains(before, k) {
			added = append(added, cat)
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	st, err := loadCatalogState(ctx, tx)
	if err != nil {
		return nil, err
	}
	var needAck []catalog.Source
	var lists []store.CatalogList
	for _, cat := range added {
		for _, src := range cat.Sources {
			l, err := st.list(cat.Key, src.Key)
			if err != nil {
				return nil, err
			}
			if l.Enabled && !src.CommercialUse {
				needAck, lists = append(needAck, src), append(lists, l)
			}
		}
	}
	if len(needAck) == 0 {
		return nil, nil
	}
	if !acknowledged {
		return nil, licenseRequired(needAck)
	}
	keys := make([]string, len(needAck))
	for i, l := range lists {
		if err := store.SetCatalogListEnabled(ctx, tx, l.ID, true, true); err != nil {
			return nil, err
		}
		keys[i] = needAck[i].Key
	}
	return keys, nil
}
