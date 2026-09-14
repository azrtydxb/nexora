package api_test

import (
	"net/http"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestVersionEndpoint(t *testing.T) {
	api.Version, api.Commit, api.BuildDate, api.RepositoryURL = "v6.0.0", "f0ee3a1c0ffee0000000000000000000000000aa", "2026-09-14T12:00:00Z", "https://github.com/azrtydxb/nexora"
	t.Cleanup(func() { api.Version, api.Commit, api.BuildDate, api.RepositoryURL = "dev", "", "", "" })
	e := newAPI(t)
	c := e.client(t)
	if code := c.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	for i, v := range []string{"sha-f0ee3a1", "sha-f0ee3a1", "sha-0000001"} {
		id := storetest.InsertEngine(t, e.st, "v"+string(rune('a'+i)), store.DefaultEngineGroupID)
		if _, err := e.st.Pool.Exec(e.ctx, `update engines set engine_version = $2 where id = $1`, id, v); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		BuildDate     string `json:"build_date"`
		RepositoryURL string `json:"repository_url"`
		Engines       []struct {
			Version string `json:"version"`
			Count   int    `json:"count"`
		} `json:"engines"`
	}
	if code := c.do("GET", "/version", nil, &got); code != http.StatusOK {
		t.Fatalf("version -> %d", code)
	}
	if got.Version != "v6.0.0" || len(got.Commit) != 40 || got.BuildDate == "" || got.RepositoryURL == "" {
		t.Fatalf("build info: %+v", got)
	}
	if len(got.Engines) != 2 || got.Engines[0].Version != "sha-f0ee3a1" || got.Engines[0].Count != 2 {
		t.Fatalf("engine versions (most common first): %+v", got.Engines)
	}
}
