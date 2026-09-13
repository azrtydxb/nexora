package control_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/rpz"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func kekBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadKEKFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestRPZTsigLoadUnsealsKeysAndDigestTracksChanges(t *testing.T) {
	ctx := context.Background()
	pg := harness.New(t).StartPostgres()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	box := kekBox(t)
	src := control.NewRPZTsig(st, box)
	keys, digest, err := src.Load(ctx)
	if err != nil || len(keys.Keys) != 0 || digest != "" {
		t.Fatalf("empty load: %v %q %v", keys, digest, err)
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	id := uuid.New()
	primary, name, alg := "127.0.0.1:5300", "rpz-key.", "hmac-sha256"
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		env, err := box.Seal(rpz.TsigPurpose(id), secret)
		if err != nil {
			return err
		}
		_, err = store.CreateRPZZone(ctx, tx, store.RPZZone{ID: id, Name: "rpz.axfr.test.", SourceType: "transfer", PrimaryAddress: &primary,
			TSIGKeyName: &name, TSIGAlgorithm: &alg, TSIGSecretEnvelope: env, MinRefreshSeconds: 60, PolicyOverride: "given"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	keys, digest, err = src.Load(ctx)
	if err != nil || len(keys.Keys) != 1 || digest == "" {
		t.Fatalf("load: %v %q %v", keys, digest, err)
	}
	k := keys.Keys[0]
	if k.ZoneId != id.String() || k.KeyName != name || k.Algorithm != controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA256 || !bytes.Equal(k.Secret, secret) {
		t.Fatalf("key = %v", k)
	}
	var stored []byte
	if err := st.Pool.QueryRow(ctx, "select tsig_secret_envelope from rpz_zones where id = $1", id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, secret) {
		t.Fatal("plaintext TSIG secret stored in rpz_zones")
	}
	if _, _, err := control.NewRPZTsig(st, kekBox(t)).Load(ctx); !errors.Is(err, secrets.ErrKEKMismatch) {
		t.Fatalf("load under another KEK: %v, want ErrKEKMismatch", err)
	}
}
