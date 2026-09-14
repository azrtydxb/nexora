package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

// topBackend is a query log backend whose Top returns entries or err.
type topBackend struct {
	querylog.Noop
	err   error
	calls []querylog.TopQuery
}

func (b *topBackend) Top(_ context.Context, q querylog.TopQuery) ([]querylog.TopEntry, error) {
	b.calls = append(b.calls, q)
	if b.err != nil {
		return nil, b.err
	}
	return []querylog.TopEntry{{Key: string(q.Field), Count: int64(q.Limit)}}, nil
}

type topEntry struct {
	Key   string
	Count int64
}

type dashboardTop struct {
	Range          string
	Available      bool
	Domains        []topEntry
	BlockedDomains []topEntry `json:"blocked_domains"`
	Clients        []topEntry
	Categories     []topEntry
}

func topClient(t *testing.T, backend querylog.Backend) *client {
	e := newAPIWith(t, func(d *api.Deps) { d.QueryLog = backend })
	c := e.client(t)
	if code := c.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	return c
}

func TestDashboardTopBackendUnavailable(t *testing.T) {
	// Positive path first: a working Topper fills all four lists.
	ok := &topBackend{}
	c := topClient(t, ok)
	var top dashboardTop
	if code := c.do("GET", "/dashboard/top?range=1h&limit=3", nil, &top); code != http.StatusOK || !top.Available ||
		len(top.Domains) != 1 || top.Domains[0].Key != "name" || top.Domains[0].Count != 3 ||
		len(top.BlockedDomains) != 1 || len(top.Clients) != 1 || top.Clients[0].Key != "client" || len(top.Categories) != 1 || top.Range != "1h" {
		t.Fatalf("available top -> %d %+v", code, top)
	}
	if len(ok.calls) != 4 || len(ok.calls[1].Filters) != 1 || ok.calls[1].Filters[0] != "blocked" || ok.calls[0].From.IsZero() {
		t.Fatalf("top calls: %+v", ok.calls)
	}
	if code := c.do("GET", "/dashboard/top?range=2d", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("range=2d -> %d, want 400", code)
	}

	c = topClient(t, &topBackend{err: querylog.ErrBackendUnavailable})
	top = dashboardTop{}
	if code := c.do("GET", "/dashboard/top?range=1h", nil, &top); code != http.StatusOK || top.Available ||
		top.Domains == nil || len(top.Domains) != 0 || len(top.Categories) != 0 {
		t.Fatalf("unavailable backend -> %d %+v", code, top)
	}

	c = topClient(t, querylog.Noop{})
	top = dashboardTop{Available: true}
	if code := c.do("GET", "/dashboard/top?range=7d", nil, &top); code != http.StatusOK || top.Available {
		t.Fatalf("noop backend -> %d %+v", code, top)
	}
}
