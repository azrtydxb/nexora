package control_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestConsumeJoinToken(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	dir := t.TempDir()
	if err := pki.InitCA(dir); err != nil {
		t.Fatal(err)
	}
	ca, err := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	var edge uuid.UUID
	if err := st.Pool.QueryRow(ctx, `insert into engine_groups (name) values ('edge') returning id`).Scan(&edge); err != nil {
		t.Fatal(err)
	}
	two := 2
	create := func(spec control.JoinTokenSpec) (string, string) {
		var id, secret string
		err := st.InTx(ctx, func(tx pgx.Tx) error {
			jt, token, err := control.CreateJoinTokenFor(ctx, tx, ca, spec)
			id = jt.ID
			secret, _, _ = pki.ParseJoinToken(token)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id, secret
	}
	consume := func(secret string) (fleet.JoinTokenGrant, error) {
		var g fleet.JoinTokenGrant
		err := st.InTx(ctx, func(tx pgx.Tx) error {
			var err error
			g, err = fleet.ConsumeJoinToken(ctx, tx, secret)
			return err
		})
		return g, err
	}

	_, secret := create(control.JoinTokenSpec{Name: "edge", CreatedBy: "t", TTL: time.Hour, EngineGroupID: edge, MaxUses: &two, Labels: map[string]string{"site": "lab"}})
	for i := 0; i < 2; i++ {
		g, err := consume(secret)
		if err != nil || g.EngineGroupID != edge || g.Labels["site"] != "lab" {
			t.Fatalf("use %d: %+v %v", i+1, g, err)
		}
	}
	if _, err := consume(secret); !errors.Is(err, fleet.ErrJoinTokenExhausted) || err.Error() != "join token exhausted" {
		t.Fatalf("third use: %v", err)
	}

	_, unlimited := create(control.JoinTokenSpec{Name: "fleet", CreatedBy: "t", TTL: time.Hour, EngineGroupID: store.DefaultEngineGroupID})
	for i := 0; i < 5; i++ {
		if _, err := consume(unlimited); err != nil {
			t.Fatalf("unlimited token use %d: %v", i+1, err)
		}
	}
	expID, expired := create(control.JoinTokenSpec{Name: "old", CreatedBy: "t", TTL: time.Hour, EngineGroupID: edge})
	if _, err := st.Pool.Exec(ctx, `update join_tokens set expires_at = now() - interval '1 second' where id = $1`, expID); err != nil {
		t.Fatal(err)
	}
	if _, err := consume(expired); !errors.Is(err, fleet.ErrJoinTokenExpired) || err.Error() != "join token expired" {
		t.Fatalf("expired: %v", err)
	}
	revID, revoked := create(control.JoinTokenSpec{Name: "revoked", CreatedBy: "t", TTL: time.Hour, EngineGroupID: edge})
	if _, err := st.Pool.Exec(ctx, `update join_tokens set revoked_at = now() where id = $1`, revID); err != nil {
		t.Fatal(err)
	}
	if _, err := consume(revoked); !errors.Is(err, fleet.ErrJoinTokenRevoked) {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := consume("AAAAAAAA"); !errors.Is(err, fleet.ErrJoinTokenUnknown) || err.Error() != "join token unknown" {
		t.Fatalf("unknown: %v", err)
	}
}
