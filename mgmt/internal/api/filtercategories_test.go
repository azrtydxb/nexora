package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
)

type categoryOut struct {
	Key      string `json:"key"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
	Sources  []struct {
		Key           string `json:"key"`
		URL           string `json:"url"`
		License       string `json:"license"`
		Attribution   string `json:"attribution"`
		CommercialUse bool   `json:"commercial_use"`
		Notice        string `json:"notice"`
		Enabled       bool   `json:"enabled"`
		ListID        string `json:"list_id"`
	} `json:"sources"`
}

func categoryClients(t *testing.T) (*client, *client, *apiEnv) {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	op, viewer, e := roleClientsWith(t, func(d *api.Deps) { d.Catalog = cat })
	if _, err := catalog.Sync(context.Background(), e.st, snapshot.BuildConfig{}, cat, catalog.Raw()); err != nil {
		t.Fatal(err)
	}
	return op, viewer, e
}

func findCategory(t *testing.T, c *client, key string) categoryOut {
	t.Helper()
	var all []categoryOut
	if code := c.do(http.MethodGet, "/filter-categories", nil, &all); code != http.StatusOK {
		t.Fatalf("list categories -> %d", code)
	}
	for _, x := range all {
		if x.Key == key {
			return x
		}
	}
	t.Fatalf("category %s missing", key)
	return categoryOut{}
}

func TestFilterCategoriesAPI(t *testing.T) {
	op, viewer, e := categoryClients(t)
	gambling := findCategory(t, viewer, "gambling")
	if gambling.Enabled || len(gambling.Sources) != 4 || gambling.Sources[0].License == "" || gambling.Sources[0].ListID == "" {
		t.Fatalf("viewer sees the catalog: %+v", gambling)
	}
	if code := viewer.do(http.MethodPut, "/filter-categories/gambling", map[string]any{"enabled": true, "revision": gambling.Revision}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer update -> %d", code)
	}
	var updated categoryOut
	if code := op.do(http.MethodPut, "/filter-categories/gambling", map[string]any{"enabled": true, "revision": gambling.Revision,
		"sources": []map[string]any{{"key": "blp-gambling", "enabled": false}}}, &updated); code != http.StatusOK || !updated.Enabled || updated.Revision != gambling.Revision+1 {
		t.Fatalf("enable gambling -> %d %+v", code, updated)
	}
	for _, s := range updated.Sources {
		if (s.Key == "blp-gambling") == s.Enabled {
			t.Fatalf("source toggles: %+v", updated.Sources)
		}
	}
	if code := op.do(http.MethodPut, "/filter-categories/gambling", map[string]any{"enabled": false, "revision": gambling.Revision}, nil); code != http.StatusConflict {
		t.Fatalf("stale revision -> %d", code)
	}

	ads := findCategory(t, op, "ads-tracking")
	var e422 apiErr
	if code := op.do(http.MethodPut, "/filter-categories/ads-tracking", map[string]any{"enabled": true, "revision": ads.Revision}, &e422); code != http.StatusUnprocessableEntity || e422.Code != "license_acknowledgement_required" {
		t.Fatalf("OISD without acknowledgement -> %d %+v", code, e422)
	}
	if findCategory(t, op, "ads-tracking").Enabled {
		t.Fatal("a rejected update enabled the category")
	}
	if code := op.do(http.MethodPut, "/filter-categories/ads-tracking", map[string]any{"enabled": true, "revision": ads.Revision, "acknowledge_license": true}, nil); code != http.StatusOK {
		t.Fatalf("OISD with acknowledgement -> %d", code)
	}
	var diff []byte
	if err := e.st.Pool.QueryRow(context.Background(), `select diff from audit_log where action = 'updateFilterCategory' and target_id = 'ads-tracking' order by id desc limit 1`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	var d struct {
		After struct {
			Acknowledged []string `json:"acknowledged_licenses"`
		} `json:"after"`
	}
	if json.Unmarshal(diff, &d) != nil || len(d.After.Acknowledged) != 1 || d.After.Acknowledged[0] != "oisd-big" {
		t.Fatalf("audit diff %s", diff)
	}

	// The catalog is read-only.
	ads = findCategory(t, op, "ads-tracking")
	if code := op.do(http.MethodPut, "/filter-categories/ads-tracking", map[string]any{"enabled": true, "revision": ads.Revision,
		"sources": []map[string]any{{"key": "my-own-source", "enabled": true}}}, &e422); code != http.StatusUnprocessableEntity || e422.Code != "unknown_source" {
		t.Fatalf("custom source -> %d %+v", code, e422)
	}
	for _, m := range []string{http.MethodPost, http.MethodDelete} {
		if code := op.do(m, "/filter-categories", map[string]any{"key": "mine"}, nil); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
			t.Fatalf("%s /filter-categories -> %d", m, code)
		}
	}
	if code := op.do(http.MethodPost, "/filter-categories/gambling/sources", map[string]any{"key": "mine", "url": "https://x.test/l.txt"}, nil); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("POST sources -> %d", code)
	}
	managed := gambling.Sources[0].ListID
	if code := op.do(http.MethodPut, "/filter-lists/"+managed, map[string]any{"name": "x", "kind": "block", "url": "https://evil.test/l.txt", "refresh_interval_seconds": 3600, "enabled": true, "revision": 1}, &e422); code != http.StatusUnprocessableEntity || e422.Code != "catalog_managed" {
		t.Fatalf("edit a catalog list -> %d %+v", code, e422)
	}
	if code := op.do(http.MethodDelete, "/filter-lists/"+managed+"?revision=1", nil, &e422); code != http.StatusUnprocessableEntity || e422.Code != "catalog_managed" {
		t.Fatalf("delete a catalog list -> %d %+v", code, e422)
	}
	if code := op.do(http.MethodPost, "/filter-lists", map[string]any{"name": "catalog:gambling:mine", "kind": "block", "url": "https://x.test/l.txt", "refresh_interval_seconds": 3600, "enabled": true}, nil); code != http.StatusBadRequest {
		t.Fatalf("reserved list name -> %d", code)
	}
	if src := findCategory(t, op, "gambling").Sources; len(src) != 4 || src[0].URL != gambling.Sources[0].URL {
		t.Fatalf("sources changed: %+v", src)
	}

	// Policy groups: category keys, the license guard and no catalog lists by id.
	group := map[string]any{"name": "kids", "cidrs": []string{"10.50.0.0/16"}, "category_keys": []string{"adult"}}
	if code := op.do(http.MethodPost, "/policy-groups", group, &e422); code != http.StatusUnprocessableEntity || e422.Code != "license_acknowledgement_required" {
		t.Fatalf("group with OISD-backed adult without acknowledgement -> %d %+v", code, e422)
	}
	group["acknowledge_license"] = true
	var created struct {
		CategoryKeys []string `json:"category_keys"`
	}
	if code := op.do(http.MethodPost, "/policy-groups", group, &created); code != http.StatusCreated || len(created.CategoryKeys) != 1 {
		t.Fatalf("group with acknowledgement -> %d %+v", code, created)
	}
	if code := op.do(http.MethodPost, "/policy-groups", map[string]any{"name": "k2", "cidrs": []string{"10.51.0.0/16"}, "category_keys": []string{"no-such"}}, &e422); code != http.StatusUnprocessableEntity || e422.Code != "unknown_category" {
		t.Fatalf("unknown category -> %d %+v", code, e422)
	}
	if code := op.do(http.MethodPost, "/policy-groups", map[string]any{"name": "k3", "cidrs": []string{"10.52.0.0/16"}, "filter_list_ids": []string{managed}}, &e422); code != http.StatusUnprocessableEntity {
		t.Fatalf("catalog list by id -> %d", code)
	}
}

func TestEngineGroupFilterIndexMaxBytes(t *testing.T) {
	op, _, _ := categoryClients(t)
	var g struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Max      int64  `json:"filter_index_max_bytes"`
	}
	if code := op.do(http.MethodPost, "/engine-groups", map[string]any{"name": "edge", "filter_index_max_bytes": 1 << 20}, nil); code != http.StatusBadRequest {
		t.Fatalf("1 MiB cap -> %d", code)
	}
	if code := op.do(http.MethodPost, "/engine-groups", map[string]any{"name": "edge", "filter_index_max_bytes": 64 << 20}, &g); code != http.StatusCreated || g.Max != 64<<20 {
		t.Fatalf("64 MiB cap -> %d %+v", code, g)
	}
}
