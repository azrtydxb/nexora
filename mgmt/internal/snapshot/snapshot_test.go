package snapshot_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func newStore(t *testing.T) (*store.Store, context.Context) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

func TestMutateWritesRowsAuditVersionAndNotifies(t *testing.T) {
	st, ctx := newStore(t)
	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "listen "+snapshot.NotifyChannel); err != nil {
		t.Fatal(err)
	}
	actor := auth.Actor{Type: "user", ID: "u-1", Name: "alice"}
	v, err := snapshot.Mutate(ctx, st, snapshot.BuildConfig{}, actor, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(ctx, "insert into upstreams(name, protocol, address, position) values ('quad9','udp','9.9.9.9:53',0)")
		return auth.Change{Action: "createUpstream", TargetType: "upstream", TargetID: "quad9", After: map[string]any{"name": "quad9"}}, err
	})
	if err != nil || v != 1 {
		t.Fatalf("mutate: v=%d err=%v", v, err)
	}
	n, err := conn.Conn().WaitForNotification(ctx)
	if err != nil || n.Payload != "1" {
		t.Fatalf("notification: %v %v", n, err)
	}
	var diff []byte
	var cv int64
	if err := st.Pool.QueryRow(ctx, "select diff, config_version from audit_log where action='createUpstream' and actor_name='alice'").Scan(&diff, &cv); err != nil || cv != 1 {
		t.Fatalf("audit row: %v cv=%d", err, cv)
	}
	var d map[string]any
	_ = json.Unmarshal(diff, &d)
	if d["after"] == nil {
		t.Fatalf("diff missing after: %s", diff)
	}
	ver, snap, err := snapshot.Latest(ctx, st.Pool)
	if err != nil || ver != 1 || len(snap.Upstreams) != 1 || snap.Upstreams[0].Address != "9.9.9.9:53" {
		t.Fatalf("latest: %d %v %v", ver, snap, err)
	}

	_, err = snapshot.Mutate(ctx, st, snapshot.BuildConfig{}, actor, func(tx pgx.Tx) (auth.Change, error) {
		_, _ = tx.Exec(ctx, "delete from upstreams")
		return auth.Change{}, store.ErrConflict
	})
	if err == nil {
		t.Fatal("failed mutation returned nil")
	}
	var rows, versions int
	_ = st.Pool.QueryRow(ctx, "select count(*) from upstreams").Scan(&rows)
	_ = st.Pool.QueryRow(ctx, "select count(*) from config_versions").Scan(&versions)
	if rows != 1 || versions != 1 {
		t.Fatalf("failed mutation leaked: upstreams=%d versions=%d", rows, versions)
	}
}

func TestBuildMapsEveryTable(t *testing.T) {
	st, ctx := newStore(t)
	sha := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	stmts := []struct {
		sql  string
		args []any
	}{
		{"insert into upstreams(name, protocol, address, tls_server_name, timeout_ms, position, enabled) values ('b','dot','9.9.9.9:853','dns.quad9.net',300,1,true), ('a','udp','1.1.1.1:53','',250,0,true), ('off','udp','8.8.8.8:53','',250,2,false)", nil},
		{"update resolver_settings set strategy='fastest', cache_max_bytes=2097152, block_mode='nxdomain', block_ttl=30, trace_sample_one_in=100", nil},
		{"update access_control set allow_cidrs=array['10.0.0.0/8']::cidr[]", nil},
		{"insert into blobs(sha256, size, data) values ($1, 3, 'abc')", []any{sha}},
		{"insert into filter_lists(name, kind, url, current_blob_sha256) values ('ads','block','http://x/ads', $1), ('empty','block','http://x/e', null)", []any{sha}},
		{"update allowlist set domains=array['good.example']", nil},
	}
	for _, q := range stmts {
		if _, err := st.Pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	var snap *controlv1.ConfigSnapshot
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		var e error
		snap, e = snapshot.Build(ctx, tx, 7, snapshot.BuildConfig{QueryLogToManagement: true, DefaultOTLPEndpoint: "http://otel:4317"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != 7 || len(snap.Upstreams) != 2 || snap.Upstreams[0].Name != "a" || snap.Upstreams[1].Protocol != controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOT {
		t.Fatalf("upstreams: %v", snap.Upstreams)
	}
	if snap.Resolver.Strategy != controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_FASTEST || snap.Cache.MaxBytes != 2097152 {
		t.Fatalf("resolver/cache: %v %v", snap.Resolver, snap.Cache)
	}
	if len(snap.AclAllowCidrs) != 1 || snap.AclAllowCidrs[0] != "10.0.0.0/8" {
		t.Fatalf("acl: %v", snap.AclAllowCidrs)
	}
	if len(snap.Filter.Blocklists) != 1 || snap.Filter.Blocklists[0].Sha256 != sha || snap.Filter.BlockMode != controlv1.BlockMode_BLOCK_MODE_NXDOMAIN || snap.Filter.BlockTtl != 30 {
		t.Fatalf("filter: %v", snap.Filter)
	}
	if len(snap.Filter.Allowlists) != 1 {
		t.Fatalf("allowlist blob missing: %v", snap.Filter.Allowlists)
	}
	var stored int
	_ = st.Pool.QueryRow(ctx, "select count(*) from blobs where sha256=$1", snap.Filter.Allowlists[0].Sha256).Scan(&stored)
	if stored != 1 {
		t.Fatal("allowlist blob not stored")
	}
	if !snap.Telemetry.QuerylogToManagement || snap.Telemetry.OtlpEndpoint != "http://otel:4317" || snap.Telemetry.TraceSampleOneIn != 100 {
		t.Fatalf("telemetry: %v", snap.Telemetry)
	}
}
