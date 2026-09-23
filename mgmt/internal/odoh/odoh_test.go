package odoh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func testBox(t *testing.T) *secrets.Box {
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

func TestOdohKeyRotation(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	k := &Keys{Pool: st.Pool, Box: testBox(t), Now: func() time.Time { return now }}
	if rotated, err := k.Rotate(ctx, nil); err != nil || rotated {
		t.Fatalf("target off must not create keys: %v %v", rotated, err)
	}
	if _, err := st.Pool.Exec(ctx, `update odoh_settings set target_enabled = true, key_rotation_hours = 24`); err != nil {
		t.Fatal(err)
	}
	if rotated, err := k.Rotate(ctx, nil); err != nil || !rotated {
		t.Fatalf("first key: %v %v", rotated, err)
	}
	if rotated, _ := k.Rotate(ctx, nil); rotated {
		t.Fatal("rotated again before the interval")
	}
	keys, digest1, err := k.Load(ctx)
	if err != nil || len(keys.Keys) != 1 || len(keys.Keys[0].Seed) != 32 || digest1 == "" {
		t.Fatalf("load: %v %v", keys, err)
	}
	if keys.Keys[0].PublishAfterUnix != now.Add(PublishDelay).Unix() || keys.Keys[0].NotAfterUnix != now.Add(48*time.Hour).Unix() {
		t.Fatalf("timestamps %+v", keys.Keys[0])
	}
	var env []byte
	if err := st.Pool.QueryRow(ctx, `select seed_envelope from odoh_keys`).Scan(&env); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(env, []byte("NXE1")) || bytes.Contains(env, keys.Keys[0].Seed) {
		t.Fatal("seed not sealed")
	}
	now = now.Add(25 * time.Hour)
	if rotated, err := k.Rotate(ctx, nil); err != nil || !rotated {
		t.Fatalf("due rotation: %v %v", rotated, err)
	}
	keys, digest2, _ := k.Load(ctx)
	if len(keys.Keys) != 2 || keys.Keys[0].PublishAfterUnix <= keys.Keys[1].PublishAfterUnix || digest2 == digest1 {
		t.Fatalf("two keys newest first, new digest: %+v", keys.Keys)
	}
	now = now.Add(24 * time.Hour) // 49 h after the first key: expired
	if rotated, _ := k.Rotate(ctx, nil); !rotated {
		t.Fatal("third rotation")
	}
	var n int
	if err := st.Pool.QueryRow(ctx, `select count(*) from odoh_keys`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("expired key not deleted: %d %v", n, err)
	}
	now = now.Add(time.Minute) // the forced key is the newest
	if rotated, _ := k.Rotate(ctx, &auth.Actor{Type: "user", ID: "admin", Name: "admin"}); !rotated {
		t.Fatal("forced rotation")
	}
	infos, _ := k.List(ctx)
	if len(infos) != 3 {
		t.Fatalf("list: %v", infos)
	}
	// The forced rotation is audited in its own transaction, naming the actor and the new key.
	var actor, target string
	if err := st.Pool.QueryRow(ctx, `select actor_id, target_id from audit_log where action = 'rotateOdohKey'`).Scan(&actor, &target); err != nil ||
		actor != "admin" || target != infos[0].ID.String() {
		t.Fatalf("forced rotation audit: %q %q %v", actor, target, err)
	}
}

func TestValidateOdohSettings(t *testing.T) {
	ok := Settings{ProxyEnabled: true, ProxyTargets: []ProxyTarget{{Host: "odoh.example:8443"}}, ProxyTimeoutMS: 2000, KeyRotationHours: 24}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]Settings{
		"no targets": {ProxyEnabled: true, ProxyTimeoutMS: 2000, KeyRotationHours: 24},
		"scheme":     {ProxyTargets: []ProxyTarget{{Host: "https://odoh.example"}}, ProxyTimeoutMS: 2000, KeyRotationHours: 24},
		"port 0":     {ProxyTargets: []ProxyTarget{{Host: "odoh.example:0"}}, ProxyTimeoutMS: 2000, KeyRotationHours: 24},
		"bad pem":    {ProxyTargets: []ProxyTarget{{Host: "odoh.example", CAPEM: "not pem"}}, ProxyTimeoutMS: 2000, KeyRotationHours: 24},
		"timeout":    {ProxyTimeoutMS: 50, KeyRotationHours: 24},
		"rotation":   {ProxyTimeoutMS: 2000, KeyRotationHours: 721},
	} {
		if Validate(s) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if Config(Settings{}) != nil || Config(Settings{TargetEnabled: true}).GetTargetEnabled() != true {
		t.Fatal("Config must be nil only when both roles are off")
	}
}

// A settings update bumps the revision and round-trips the targets; a stale revision is a conflict.
func TestOdohSettingsRevision(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	cur, err := GetSettings(ctx, st.Pool)
	if err != nil || cur.TargetEnabled || cur.ProxyEnabled || cur.Revision != 1 {
		t.Fatalf("defaults: %+v %v", cur, err)
	}
	in := Settings{ProxyEnabled: true, ProxyTargets: []ProxyTarget{{Host: "[2001:db8::1]:8443"}}, ProxyTimeoutMS: 3000, KeyRotationHours: 12}
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UpdateSettings(ctx, tx, in, cur.Revision)
	if err != nil || tx.Commit(ctx) != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Revision != 2 || len(got.ProxyTargets) != 1 || got.ProxyTargets[0].Host != "[2001:db8::1]:8443" || got.KeyRotationHours != 12 {
		t.Fatalf("updated: %+v", got)
	}
	if c := Config(got); c == nil || len(c.ProxyTargets) != 1 || c.ProxyTimeoutMs != 3000 {
		t.Fatalf("config: %v", c)
	}
	tx, err = st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := UpdateSettings(ctx, tx, in, cur.Revision); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
}
