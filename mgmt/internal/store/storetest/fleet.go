package storetest

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// InsertEngine creates an enrolled, never-connected engine row in engineGroupID.
func InsertEngine(t *testing.T, st *store.Store, nodeName string, engineGroupID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := st.Pool.QueryRow(context.Background(), `insert into engines (node_name, certificate_serial, engine_group_id)
		values ($1, md5(random()::text), $2) returning id`, nodeName, engineGroupID).Scan(&id); err != nil {
		t.Fatalf("insert engine %s: %v", nodeName, err)
	}
	return id
}

// ConnectEngine marks the engine as holding a live stream on the fixture instance "storetest".
func ConnectEngine(t *testing.T, st *store.Store, engineID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `insert into instances (id) values ('storetest')
		on conflict (id) do update set heartbeat_at = now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update engines set connected_instance = 'storetest', last_seen_at = now() where id = $1`, engineID); err != nil {
		t.Fatal(err)
	}
}
