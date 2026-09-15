package harness

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// PGExec runs one SQL statement against the database at url, failing the test on error. AI tests
// use it to seed rows the API cannot create.
func PGExec(t *testing.T, url, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("PGExec connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("PGExec %q: %v", sql, err)
	}
}
