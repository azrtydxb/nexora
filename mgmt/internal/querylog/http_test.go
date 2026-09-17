package querylog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

func TestBackendRedirectsDoNotForwardRequests(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	ch, err := querylog.NewClickHouse(config.ClickHouseConfig{URL: origin.URL, Table: "querylog", Username: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	loki, err := querylog.NewLoki(config.LokiConfig{URL: origin.URL, Tenant: "private", Username: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []querylog.Backend{ch, loki} {
		t.Run(backend.Name(), func(t *testing.T) {
			if _, err := backend.Search(context.Background(), querylog.Query{}); err == nil {
				t.Fatal("redirect accepted")
			}
			if received.Load() != 0 {
				t.Fatal("backend request forwarded to redirect destination")
			}
		})
	}
}
