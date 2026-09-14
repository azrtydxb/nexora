package blocklist

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestFetcherUsesCatalogMirrorAndArchiveMembers(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	cat, _ := catalog.Load()
	if _, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, cat, catalog.Raw()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	served := map[string][]byte{"hagezi-gambling": []byte("casino.mirror.test\n")}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		body, ok := served[strings.TrimPrefix(r.URL.Path, "/mirror/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	f := NewFetcher(st, snapshot.BuildConfig{}, srv.Client()).WithCatalogMirror(srv.URL + "/mirror")
	id := func(source string) string {
		var v string
		if err := st.Pool.QueryRow(ctx, "select id::text from filter_lists where source_key = $1", source).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	state := func(source string) (entries int, lastError string) {
		_ = st.Pool.QueryRow(ctx, "select entry_count, last_error from filter_lists where source_key = $1", source).Scan(&entries, &lastError)
		return
	}
	if err := f.refresh(ctx, id("hagezi-gambling"), systemActor, true); err != nil {
		t.Fatal(err)
	}
	if n, e := state("hagezi-gambling"); n != 1 || e != "" || !strings.HasSuffix(paths[0], "/mirror/hagezi-gambling") {
		t.Fatalf("mirror fetch: entries=%d err=%q paths=%v", n, e, paths)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "casino.ut1.test\n"
	_ = tw.WriteHeader(&tar.Header{Name: "blacklists/gambling/domains", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	mu.Lock()
	served["ut1-gambling"], served["ut1-dating"] = buf.Bytes(), buf.Bytes()
	mu.Unlock()
	if err := f.refresh(ctx, id("ut1-gambling"), systemActor, true); err != nil {
		t.Fatal(err)
	}
	if n, e := state("ut1-gambling"); n != 1 || e != "" {
		t.Fatalf("ut1 gambling member: entries=%d err=%q", n, e)
	}
	if err := f.refresh(ctx, id("ut1-dating"), systemActor, true); err != nil {
		t.Fatal(err)
	}
	if n, e := state("ut1-dating"); n != 0 || e != "archive member blacklists/dating/domains not found" {
		t.Fatalf("missing member: entries=%d err=%q", n, e)
	}
}
