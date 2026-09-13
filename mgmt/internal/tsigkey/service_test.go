package tsigkey_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
)

var admin = auth.Actor{Type: "user", ID: "admin", Name: "admin"}

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
	box, err := secrets.Open(secrets.Config{KEKFile: p})
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestCreateStoresOnlyEnvelopeAndPublishes(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	s := &tsigkey.Service{Store: st, Box: kekBox(t)}
	var before int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM config_versions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	c, err := s.Create(ctx, admin, "Xfr-Key.", "hmac-sha256", "")
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := base64.StdEncoding.DecodeString(c.Secret)
	if len(secret) != 32 || c.Name != "xfr-key." || c.Revision != 1 {
		t.Fatalf("created key: name=%q revision=%d secret length %d", c.Name, c.Revision, len(secret))
	}
	var dump string
	if err := st.Pool.QueryRow(ctx, `SELECT string_agg(t::text, E'\n') FROM tsig_keys t`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(dump), []byte(c.Secret)) || bytes.Contains([]byte(dump), []byte(hex.EncodeToString(secret))) {
		t.Fatal("plaintext TSIG secret stored in tsig_keys")
	}
	// Audit rows and snapshots carry the key's identity, never its secret.
	var audit string
	if err := st.Pool.QueryRow(ctx, `SELECT diff::text FROM audit_log WHERE action = 'createTsigKey' AND target_id = $1`, c.ID.String()).Scan(&audit); err != nil {
		t.Fatalf("createTsigKey audit row: %v", err)
	}
	if !bytes.Contains([]byte(audit), []byte("xfr-key.")) {
		t.Fatalf("audit row does not describe the key: %s", audit)
	}
	var snap []byte
	if err := st.Pool.QueryRow(ctx, `SELECT snapshot FROM config_versions ORDER BY version DESC LIMIT 1`).Scan(&snap); err != nil {
		t.Fatal(err)
	}
	for what, blob := range map[string][]byte{"audit row": []byte(audit), "snapshot": snap} {
		if bytes.Contains(blob, secret) || bytes.Contains(blob, []byte(c.Secret)) || bytes.Contains(blob, []byte(hex.EncodeToString(secret))) {
			t.Fatalf("TSIG secret leaked into the %s", what)
		}
	}
	var after int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM config_versions`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("creating a key must publish a config version so engines get the new KeyMaterial: %d -> %d", before, after)
	}
	provided := base64.StdEncoding.EncodeToString([]byte("fixture-tsig-key-provided"))
	if p, err := s.Create(ctx, admin, "given-key.", "hmac-sha384", provided); err != nil || p.Secret != provided {
		t.Fatalf("provided secret: %+v %v", p, err)
	}
	if long, err := s.Create(ctx, admin, "long-key.", "hmac-sha512", ""); err != nil || len(mustDecode(t, long.Secret)) != 64 {
		t.Fatalf("hmac-sha512 generated secret: %v", err)
	}
	for name, in := range map[string][3]string{
		"invalid name":   {"Bad Name", "hmac-sha256", ""},
		"relative name":  {"relative", "hmac-sha256", ""},
		"hmac-md5":       {"md5-key.", "hmac-md5", ""},
		"short secret":   {"short.", "hmac-sha256", base64.StdEncoding.EncodeToString([]byte("fixture-short"))},
		"invalid base64": {"b64.", "hmac-sha256", "%%%"},
	} {
		if _, err := s.Create(ctx, admin, in[0], in[1], in[2]); !errors.Is(err, tsigkey.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.Create(ctx, admin, "xfr-key.", "hmac-sha256", ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	keys, err := s.List(ctx)
	if err != nil || len(keys) != 3 || keys[0].Name != "given-key." {
		t.Fatalf("list: %+v %v", keys, err)
	}
	name, alg, got, err := s.Secret(ctx, st.Pool, c.ID)
	if err != nil || name != "xfr-key." || alg != "hmac-sha256" || !bytes.Equal(got, secret) {
		t.Fatalf("Secret: %q %q %v", name, alg, err)
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSwappedEnvelopeOrIdentityIsRejected(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	s := &tsigkey.Service{Store: st, Box: kekBox(t)}
	a, err := s.Create(ctx, admin, "a-key.", "hmac-sha256", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, admin, "b-key.", "hmac-sha256", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Secret(ctx, st.Pool, a.ID); err != nil {
		t.Fatalf("positive path: %v", err)
	}
	// Copy b's envelope onto a: a's row identity is authenticated data, so it must not open.
	if _, err := st.Pool.Exec(ctx, `UPDATE tsig_keys SET secret_envelope = (SELECT secret_envelope FROM tsig_keys WHERE id = $2) WHERE id = $1`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Secret(ctx, st.Pool, a.ID); err == nil {
		t.Fatal("an envelope moved to another row was accepted")
	}
	// Changing b's algorithm in the database must invalidate its envelope too.
	if _, _, _, err := s.Secret(ctx, st.Pool, b.ID); err != nil {
		t.Fatalf("positive path b: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE tsig_keys SET algorithm = 'hmac-sha512' WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Secret(ctx, st.Pool, b.ID); err == nil {
		t.Fatal("an envelope opened after its row's algorithm changed")
	}
}

func TestUnconfiguredKeyStorageRefuses(t *testing.T) {
	ctx := context.Background()
	box, err := secrets.Open(secrets.Config{})
	if err != nil {
		t.Fatal(err)
	}
	st := storetest.New(t)
	s := &tsigkey.Service{Store: st, Box: box}
	if _, err := s.Create(ctx, admin, "k.", "hmac-sha256", ""); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("got %v, want ErrUnconfigured", err)
	}
	if keys, err := s.List(ctx); err != nil || len(keys) != 0 {
		t.Fatalf("a refused create must not leave a row: %v %v", keys, err)
	}
}

func TestDeleteInUseKeyFails(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	s := &tsigkey.Service{Store: st, Box: kekBox(t)}
	var ids []string
	for _, n := range []string{"transfer.", "update.", "notify.", "primary.", "free."} {
		c, err := s.Create(ctx, admin, n, "hmac-sha256", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID.String())
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO zones (name, kind, soa_mname, soa_rname, transfer_tsig_key_id, update_tsig_key_ids, notify_targets)
		VALUES ('u.test.', 'primary', 'ns.u.test.', 'h.u.test.', $1, ARRAY[$2::uuid], jsonb_build_array(jsonb_build_object('address', '192.0.2.1:53', 'tsig_key_id', $3::text)))`,
		ids[0], ids[1], ids[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO zones (name, kind, soa_mname, soa_rname, primaries)
		VALUES ('s.test.', 'secondary', 'ns.s.test.', 'h.s.test.', jsonb_build_array(jsonb_build_object('address', '192.0.2.2:53', 'tsig_key_id', $1::text)))`, ids[3]); err != nil {
		t.Fatal(err)
	}
	keys, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		err := s.Delete(ctx, admin, k.ID, k.Revision)
		switch {
		case k.Name == "free.":
			if err != nil {
				t.Fatalf("delete unused key: %v", err)
			}
		case !errors.Is(err, tsigkey.ErrInUse):
			t.Fatalf("%s: got %v, want ErrInUse", k.Name, err)
		}
	}
	if err := s.Delete(ctx, admin, keys[1].ID, keys[1].Revision+1); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	var deleted int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'deleteTsigKey'`).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("deleteTsigKey audit rows: %d %v", deleted, err)
	}
}
