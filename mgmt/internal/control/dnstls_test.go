package control_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

func pending(ch <-chan control.Delivery[*controlv1.TlsMaterial]) *controlv1.TlsMaterial {
	select {
	case m := <-ch:
		value, _ := m.Await(context.Background())
		return value
	default:
		return nil
	}
}

func TestDNSTLSFanoutPushesOnlyWhenFingerprintDiffers(t *testing.T) {
	f, register := tlsFixture(t)
	a := register(tlsID("engine-a"), "")
	if pending(a) != nil {
		t.Fatal("nothing to push before material exists")
	}
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-1"), KeyPEM: []byte("key-1"), FingerprintSHA256: "aa"})
	if m := pending(a); m == nil || m.FingerprintSha256 != "aa" || string(m.PrivateKeyPem) != "key-1" {
		t.Fatalf("engine a got %v", m)
	}
	f.Result(tlsID("engine-a"), &controlv1.TlsMaterialResult{FingerprintSha256: "aa", Applied: true})

	b := register(tlsID("engine-b"), "aa")
	c := register(tlsID("engine-c"), "old")
	if pending(b) != nil {
		t.Fatal("engine already holding the certificate must not receive it again")
	}
	if m := pending(c); m == nil || m.FingerprintSha256 != "aa" {
		t.Fatal("engine with an old certificate must receive the current one on registration")
	}
	f.Result(tlsID("engine-c"), &controlv1.TlsMaterialResult{FingerprintSha256: "aa", Applied: false, Error: "certificate expired at 1"})

	// two rotations before the sender drains: only the latest is delivered
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-2"), KeyPEM: []byte("key-2"), FingerprintSHA256: "bb"})
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-3"), KeyPEM: []byte("key-3"), FingerprintSHA256: "cc"})
	for name, ch := range map[string]<-chan control.Delivery[*controlv1.TlsMaterial]{"a": a, "b": b, "c": c} {
		if m := pending(ch); m == nil || m.FingerprintSha256 != "cc" {
			t.Fatalf("engine %s got %v, want latest cc", name, m)
		}
		if pending(ch) != nil {
			t.Fatalf("engine %s received a superseded certificate", name)
		}
	}
	// engine b reconnects before its old stream ends: ending the old stream keeps the new registration
	b2 := register(tlsID("engine-b"), "cc")
	f.Unregister(tlsID("engine-b"), b)
	f.Unregister(tlsID("engine-a"), a)
	f.Set(&pki.DNSTLSMaterial{FingerprintSHA256: "dd"})
	if pending(a) != nil {
		t.Fatal("unregistered engine must receive nothing")
	}
	if m := pending(b2); m == nil || m.FingerprintSha256 != "dd" {
		t.Fatalf("reconnected engine b got %v, want dd", m)
	}
}

func TestDNSTLSDelayedResultCannotChangeReplacement(t *testing.T) {
	f, register := tlsFixture(t)
	old := register(tlsID("engine"), "old")
	current := register(tlsID("engine"), "current")
	f.ResultFor(tlsID("engine"), old, &controlv1.TlsMaterialResult{Applied: true, FingerprintSha256: "next"})
	f.Set(&pki.DNSTLSMaterial{FingerprintSHA256: "next"})
	if m := pending(current); m == nil || m.FingerprintSha256 != "next" {
		t.Fatal("stale result suppressed current stream's material")
	}
	f.ResultFor(tlsID("engine"), current, &controlv1.TlsMaterialResult{Applied: true, FingerprintSha256: "next"})
	f.Set(&pki.DNSTLSMaterial{FingerprintSHA256: "next"})
	if pending(current) != nil {
		t.Fatal("current result was not recorded")
	}
}

// Preserve the queue behavior assertions using persisted, authenticated sessions.
func tlsFixture(t *testing.T) (*control.DNSTLSFanout, func(string, string) <-chan control.Delivery[*controlv1.TlsMaterial]) {
	st := storetest.New(t)
	f := control.NewDNSTLSFanout(st)

	return f, func(name, fingerprint string) <-chan control.Delivery[*controlv1.TlsMaterial] {
		t.Helper()
		id := name
		session := uuid.New()
		ctx := context.Background()
		if _, err := st.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values ($1,$2,'aa',$3) on conflict(id) do update set connection_session=$3`, id, name, session); err != nil {
			t.Fatal(err)
		}
		serial := strings.ReplaceAll(id, "-", "")
		if _, err := st.Pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ($1,$2,now(),now()+interval '1 hour') on conflict do nothing`, serial, id); err != nil {
			t.Fatal(err)
		}
		ch, err := f.Register(ctx, id, session, serial, fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
}

func tlsID(name string) string { return uuid.NewSHA1(uuid.Nil, []byte(name)).String() }
