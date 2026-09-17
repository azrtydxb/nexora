package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

func pending(ch <-chan *controlv1.TlsMaterial) *controlv1.TlsMaterial {
	select {
	case m := <-ch:
		return m
	default:
		return nil
	}
}

func TestDNSTLSFanoutPushesOnlyWhenFingerprintDiffers(t *testing.T) {
	f := control.NewDNSTLSFanout()
	a := f.Register("engine-a", "")
	if pending(a) != nil {
		t.Fatal("nothing to push before material exists")
	}
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-1"), KeyPEM: []byte("key-1"), FingerprintSHA256: "aa"})
	if m := pending(a); m == nil || m.FingerprintSha256 != "aa" || string(m.PrivateKeyPem) != "key-1" {
		t.Fatalf("engine a got %v", m)
	}
	f.Result("engine-a", &controlv1.TlsMaterialResult{FingerprintSha256: "aa", Applied: true})

	b := f.Register("engine-b", "aa")
	c := f.Register("engine-c", "old")
	if pending(b) != nil {
		t.Fatal("engine already holding the certificate must not receive it again")
	}
	if m := pending(c); m == nil || m.FingerprintSha256 != "aa" {
		t.Fatal("engine with an old certificate must receive the current one on registration")
	}
	f.Result("engine-c", &controlv1.TlsMaterialResult{FingerprintSha256: "aa", Applied: false, Error: "certificate expired at 1"})

	// two rotations before the sender drains: only the latest is delivered
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-2"), KeyPEM: []byte("key-2"), FingerprintSHA256: "bb"})
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-3"), KeyPEM: []byte("key-3"), FingerprintSHA256: "cc"})
	for name, ch := range map[string]<-chan *controlv1.TlsMaterial{"a": a, "b": b, "c": c} {
		if m := pending(ch); m == nil || m.FingerprintSha256 != "cc" {
			t.Fatalf("engine %s got %v, want latest cc", name, m)
		}
		if pending(ch) != nil {
			t.Fatalf("engine %s received a superseded certificate", name)
		}
	}
	// engine b reconnects before its old stream ends: ending the old stream keeps the new registration
	b2 := f.Register("engine-b", "cc")
	f.Unregister("engine-b", b)
	f.Unregister("engine-a", a)
	f.Set(&pki.DNSTLSMaterial{FingerprintSHA256: "dd"})
	if pending(a) != nil {
		t.Fatal("unregistered engine must receive nothing")
	}
	if m := pending(b2); m == nil || m.FingerprintSha256 != "dd" {
		t.Fatalf("reconnected engine b got %v, want dd", m)
	}
}

func TestDNSTLSDelayedResultCannotChangeReplacement(t *testing.T) {
	f := control.NewDNSTLSFanout()
	old := f.Register("engine", "old")
	current := f.Register("engine", "current")
	f.ResultFor("engine", old, &controlv1.TlsMaterialResult{Applied: true, FingerprintSha256: "next"})
	f.Set(&pki.DNSTLSMaterial{FingerprintSHA256: "next"})
	if m := pending(current); m == nil || m.FingerprintSha256 != "next" {
		t.Fatal("stale result suppressed current stream's material")
	}
	f.ResultFor("engine", current, &controlv1.TlsMaterialResult{Applied: true, FingerprintSha256: "next"})
	f.Set(&pki.DNSTLSMaterial{FingerprintSHA256: "next"})
	if pending(current) != nil {
		t.Fatal("current result was not recorded")
	}
}
