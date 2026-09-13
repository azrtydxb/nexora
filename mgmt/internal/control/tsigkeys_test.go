package control_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
)

func TestTSIGKeysLoadUnsealsAndDigestTracksChanges(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	box := kekBox(t) // M3 helper in rpztsig_test.go
	admin := auth.Actor{Type: "user", ID: "admin", Name: "admin"}
	loader := control.NewTSIGKeys(st, box)
	if km, digest, err := loader.Load(ctx); err != nil || len(km.TsigKeys) != 0 || digest != "" {
		t.Fatalf("empty set: %v %q %v", km, digest, err)
	}
	s := &tsigkey.Service{Store: st, Box: box}
	secret := base64.StdEncoding.EncodeToString([]byte("fixture-tsig-key-xfr"))
	if _, err := s.Create(ctx, admin, "xfr-key.", "hmac-sha512", secret); err != nil {
		t.Fatal(err)
	}
	km, first, err := loader.Load(ctx)
	if err != nil || len(km.TsigKeys) != 1 || first == "" {
		t.Fatalf("one key: %q %v", first, err)
	}
	k := km.TsigKeys[0]
	if k.Name != "xfr-key." || k.Algorithm != controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA512 || !bytes.Equal(k.Secret, []byte("fixture-tsig-key-xfr")) {
		t.Fatalf("key material: name=%q alg=%v", k.Name, k.Algorithm)
	}
	if _, again, _ := loader.Load(ctx); again != first {
		t.Fatal("digest of an unchanged key set changed")
	}
	if _, err := s.Create(ctx, admin, "second.", "hmac-sha384", ""); err != nil {
		t.Fatal(err)
	}
	km, second, err := loader.Load(ctx)
	if err != nil || second == first || len(km.TsigKeys) != 2 || km.TsigKeys[0].Algorithm != controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA384 {
		t.Fatalf("digest did not change with the key set: %v", err)
	}
	if _, _, err := control.NewTSIGKeys(st, kekBox(t)).Load(ctx); !errors.Is(err, secrets.ErrKEKMismatch) {
		t.Fatalf("load under another KEK: %v, want ErrKEKMismatch", err)
	}
}

func TestKeyMaterialIsSentBeforeTheSnapshotAndOnChange(t *testing.T) {
	box := kekBox(t)
	f := setupHub(t, 1, func(st *store.Store, h *control.Hub) { h.TSIGKeys = control.NewTSIGKeys(st, box) })
	admin := auth.Actor{Type: "user", ID: "admin", Name: "admin"}
	s := &tsigkey.Service{Store: f.st, Box: box}
	if _, err := s.Create(f.ctx, admin, "first.", "hmac-sha256", ""); err != nil {
		t.Fatal(err)
	}
	client, id := f.enroll(t, f.addr[0])
	stream, err := client.Connect(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "e1"}}})
	msgs := make(chan *controlv1.ServerMessage, 16)
	go func() {
		for {
			m, err := stream.Recv()
			if err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()
	next := func() *controlv1.ServerMessage {
		t.Helper()
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatal("stream closed")
			}
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no message within 5s")
		}
		return nil
	}
	if km := next().GetKeyMaterial(); km == nil || len(km.TsigKeys) != 1 || km.TsigKeys[0].Name != "first." || len(km.TsigKeys[0].Secret) != 32 {
		t.Fatalf("first message must be the key material: %v", km != nil)
	}
	if snap := next().GetSnapshot(); snap == nil {
		t.Fatal("second message must be the snapshot")
	}
	if _, err := s.Create(f.ctx, admin, "second.", "hmac-sha256", ""); err != nil {
		t.Fatal(err)
	}
	gotKeys, gotSnap := false, false
	for !gotKeys || !gotSnap {
		m := next()
		if km := m.GetKeyMaterial(); km != nil {
			if gotKeys || len(km.TsigKeys) != 2 {
				t.Fatalf("key material after create: duplicate=%v keys=%d", gotKeys, len(km.TsigKeys))
			}
			gotKeys = true
		}
		gotSnap = gotSnap || m.GetSnapshot() != nil
	}
}
