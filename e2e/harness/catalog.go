package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// CatalogMirrorEnv is the management environment entry that fetches every catalog source from
// the fixture at <base>/lists/<source key> (never the internet).
func CatalogMirrorEnv(f *HTTPFixture) string { return "NEXORA_CATALOG_MIRROR=" + f.Base + "/lists" }

type CategorySource struct {
	Key           string `json:"key"`
	URL           string `json:"url"`
	License       string `json:"license"`
	Attribution   string `json:"attribution"`
	CommercialUse bool   `json:"commercial_use"`
	Notice        string `json:"notice"`
	Enabled       bool   `json:"enabled"`
	ListID        string `json:"list_id"`
	EntryCount    int    `json:"entry_count"`
	LastError     string `json:"last_error"`
	Stale         bool   `json:"stale"`
}

type CategoryView struct {
	Key      string           `json:"key"`
	Enabled  bool             `json:"enabled"`
	Stale    bool             `json:"stale"`
	Revision int64            `json:"revision"`
	Sources  []CategorySource `json:"sources"`
}

// ListState is the refresh answer of a filter list.
type ListState struct {
	EntryCount int    `json:"entry_count"`
	LastError  string `json:"last_error"`
	Stale      bool   `json:"stale"`
}

func (a *API) FilterCategory(key string) CategoryView {
	a.T.Helper()
	var all []CategoryView
	a.Must(http.MethodGet, "/filter-categories", nil, &all, http.StatusOK)
	for _, c := range all {
		if c.Key == key {
			return c
		}
	}
	a.T.Fatalf("filter category %s missing", key)
	return CategoryView{}
}

// SetFilterCategory enables or disables a category (and sources) at its current revision and
// returns the status and the error code of a refusal.
func (a *API) SetFilterCategory(key string, enabled bool, sources map[string]bool, acknowledge bool) (int, string) {
	a.T.Helper()
	body := map[string]any{"enabled": enabled, "revision": a.FilterCategory(key).Revision, "acknowledge_license": acknowledge}
	if len(sources) > 0 {
		var toggles []map[string]any
		for k, v := range sources {
			toggles = append(toggles, map[string]any{"key": k, "enabled": v})
		}
		body["sources"] = toggles
	}
	status, err := a.Do(http.MethodPut, "/filter-categories/"+key, body, nil)
	if err == nil {
		return status, ""
	}
	if status == 0 {
		a.T.Fatalf("PUT /filter-categories/%s: %v", key, err)
	}
	var e struct {
		Code string `json:"code"`
	}
	if msg := err.Error(); strings.Contains(msg, "{") {
		_ = json.Unmarshal([]byte(msg[strings.Index(msg, "{"):]), &e)
	}
	return status, e.Code
}

// RefreshSource fetches one catalog source now and returns the list state.
func (a *API) RefreshSource(category, source string) ListState {
	a.T.Helper()
	for _, s := range a.FilterCategory(category).Sources {
		if s.Key == source {
			var st ListState
			a.Must(http.MethodPost, "/filter-lists/"+s.ListID+"/refresh", nil, &st, http.StatusOK)
			return st
		}
	}
	a.T.Fatalf("source %s missing in category %s", source, category)
	return ListState{}
}

// RefreshCategory fetches every source of the category whose own enabled flag is set.
func (a *API) RefreshCategory(category string) {
	a.T.Helper()
	for _, s := range a.FilterCategory(category).Sources {
		if s.Enabled {
			a.Must(http.MethodPost, "/filter-lists/"+s.ListID+"/refresh", nil, nil, http.StatusOK)
		}
	}
}

// UT1Archive builds a blacklists.tar.gz with the given members.
func UT1Archive(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
