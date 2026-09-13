// Package storetest opens a fresh, migrated database for package tests.
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// New starts a PostgreSQL instance for this test, migrates it and returns the store.
func New(t *testing.T) *store.Store {
	t.Helper()
	pg := harness.New(t).StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
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
