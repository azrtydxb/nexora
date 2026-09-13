package e2e

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

type dnssecKey struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Algorithm uint8  `json:"algorithm"`
	KeyTag    uint16 `json:"key_tag"`
	Flags     uint16 `json:"flags"`
	State     string `json:"state"`
	DSState   string `json:"ds_state"`
	PublicKey string `json:"public_key"`
}

type dnssecView struct {
	Enabled bool        `json:"enabled"`
	Keys    []dnssecKey `json:"keys"`
	DS      []string    `json:"ds"`
}

func dnssecState(t *testing.T, api *harness.API, zoneID string) dnssecView {
	t.Helper()
	var v dnssecView
	api.Must(http.MethodGet, "/zones/"+zoneID+"/dnssec", nil, &v, http.StatusOK)
	return v
}

func keysWith(v dnssecView, role, state string) []dnssecKey {
	var out []dnssecKey
	for _, k := range v.Keys {
		if k.Role == role && k.State == state {
			out = append(out, k)
		}
	}
	return out
}

func anchorsFor(t *testing.T, zone string, keys []dnssecKey) string {
	var a []harness.TrustAnchor
	for _, k := range keys {
		a = append(a, harness.TrustAnchor{Flags: 257, Protocol: 3, Algorithm: k.Algorithm, PublicKey: k.PublicKey})
	}
	return harness.WriteTrustAnchors(t, zone, a)
}

func createSignedZone(t *testing.T, api *harness.API, name, backend string) (string, dnssecView) {
	t.Helper()
	var z zoneResp
	api.Must(http.MethodPost, "/zones", map[string]any{
		"name": name, "kind": "primary", "default_ttl": 2,
		"soa":         map[string]any{"mname": "ns1." + name, "rname": "hostmaster." + name, "ttl": 2, "minimum": 2},
		"nameservers": []string{"ns1." + name},
	}, &z, http.StatusCreated)
	for _, r := range [][3]string{{"ns1." + name, "A", "192.0.2.1"}, {"www." + name, "A", "192.0.2.10"}, {"*.wild." + name, "TXT", "\"wild\""}} {
		api.Must(http.MethodPost, "/zones/"+z.ID+"/records", map[string]any{"name": r[0], "type": r[1], "ttl": 2, "data": r[2]}, nil, http.StatusCreated)
	}
	api.Must(http.MethodGet, "/zones/"+z.ID, nil, &z, http.StatusOK)
	api.Must(http.MethodPut, "/zones/"+z.ID+"/dnssec", map[string]any{
		"revision": z.Revision, "enabled": true, "algorithm": 13, "nsec_mode": "nsec3", "key_backend": backend,
		"propagation_delay_seconds": 2, "parent_ds_ttl_seconds": 2, "zsk_lifetime_days": 0,
	}, nil, http.StatusOK)
	return z.ID, dnssecState(t, api, z.ID)
}

func validates(t *testing.T, server, anchors, zone, stage string) {
	t.Helper()
	root := strings.TrimSuffix(zone, ".")
	for _, q := range []struct{ name, qtype, want string }{
		{"www." + zone, "A", "; fully validated"},
		{"nosuch." + zone, "A", "; negative response, fully validated"},
		{"x.wild." + zone, "TXT", "; fully validated"},
	} {
		out := harness.Delv(t, server, anchors, root, q.name, q.qtype)
		// delv prints ";; resolution failed: ncache nxdomain" above a validated denial, so only a
		// whole "; fully validated" line counts, and "failed" is fatal for positive answers only.
		failed := strings.Contains(out, "resolution failed") && !strings.Contains(out, "resolution failed: ncache")
		if !strings.Contains("\n"+out, "\n"+q.want+"\n") || failed {
			t.Fatalf("%s: delv %s %s:\n%s", stage, q.name, q.qtype, out)
		}
	}
}

func queryDO(t *testing.T, server, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return harness.MustQuery(t, server, name, qtype, harness.QueryOpts{TCP: true, DO: true, EDNSSize: 4096})
}

func TestDNSSECSigningRollover(t *testing.T) {
	e := startAuthEnv(t, []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}, "sign-1")
	api, eng := e.api, e.engines[0]

	zoneID, st := createSignedZone(t, api, "signed.test.", "kek")
	oldKSK := keysWith(st, "ksk", "active")
	oldZSK := keysWith(st, "zsk", "active")
	if len(oldKSK) != 1 || len(oldZSK) != 1 || len(st.DS) != 1 {
		t.Fatalf("initial keys: %+v", st)
	}
	anchors := anchorsFor(t, "signed.test.", oldKSK)
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		return strings.Contains(harness.Delv(t, eng.DNS, anchors, "signed.test", "www.signed.test.", "A"), "; fully validated")
	}, "signed.test validates on the engine")
	validates(t, eng.DNS, anchors, "signed.test.", "initial")

	// ZSK pre-publish rollover; validation must hold at every intermediate state
	api.Must(http.MethodPost, "/zones/"+zoneID+"/dnssec/rollovers", map[string]any{"role": "zsk"}, nil, http.StatusAccepted)
	deadline := time.Now().Add(120 * time.Second)
	var newZSK dnssecKey
	for {
		st = dnssecState(t, api, zoneID)
		validates(t, eng.DNS, anchors, "signed.test.", fmt.Sprintf("zsk rollover %+v", st.Keys))
		active := keysWith(st, "zsk", "active")
		removed := keysWith(st, "zsk", "removed")
		if len(active) == 1 && active[0].ID != oldZSK[0].ID && len(removed) == 1 && removed[0].ID == oldZSK[0].ID {
			newZSK = active[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ZSK rollover did not complete: %+v", st.Keys)
		}
		time.Sleep(2 * time.Second)
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		for _, rr := range queryDO(t, eng.DNS, "www.signed.test.", dns.TypeA).Answer {
			if s, ok := rr.(*dns.RRSIG); ok && s.KeyTag == newZSK.KeyTag {
				return true
			}
		}
		return false
	}, "www.signed.test. signed by the new ZSK")
	validates(t, eng.DNS, anchors, "signed.test.", "after zsk rollover")

	// KSK double-signature rollover with CDS publication
	api.Must(http.MethodPost, "/zones/"+zoneID+"/dnssec/rollovers", map[string]any{"role": "ksk"}, nil, http.StatusAccepted)
	st = dnssecState(t, api, zoneID)
	var newKSK dnssecKey
	for _, k := range keysWith(st, "ksk", "active") {
		if k.ID != oldKSK[0].ID {
			newKSK = k
		}
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		m := harness.MustQuery(t, eng.DNS, "signed.test.", dns.TypeCDS, harness.QueryOpts{TCP: true})
		if len(m.Answer) != 1 {
			return false
		}
		cds, ok := m.Answer[0].(*dns.CDS)
		return ok && cds.KeyTag == newKSK.KeyTag
	}, "CDS advertises the new KSK")
	newAnchors := anchorsFor(t, "signed.test.", []dnssecKey{newKSK})
	validates(t, eng.DNS, anchors, "signed.test.", "double signature, old anchor")
	validates(t, eng.DNS, newAnchors, "signed.test.", "double signature, new anchor")
	api.Must(http.MethodPost, "/zones/"+zoneID+"/dnssec/rollovers/ds-published", map[string]any{"key_id": newKSK.ID}, nil, http.StatusOK)
	harness.EventuallyTrue(t, 60*time.Second, func() bool {
		return len(keysWith(dnssecState(t, api, zoneID), "ksk", "removed")) == 1
	}, "old KSK removed")
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		return len(harness.MustQuery(t, eng.DNS, "signed.test.", dns.TypeDNSKEY, harness.QueryOpts{TCP: true}).Answer) == 2
	}, "DNSKEY RRset holds the new KSK and ZSK only")
	validates(t, eng.DNS, newAnchors, "signed.test.", "after ksk rollover")
}

func TestKeyStorageBackends(t *testing.T) {
	kekFile := harness.WriteKEK(t)
	hsm := harness.InitSoftHSM(t, "nexora-e2e")
	e := startAuthEnv(t, append([]string{"NEXORA_KEK_FILE=" + kekFile}, hsm.Env()...), "keys-1")
	api, eng := e.api, e.engines[0]

	var key tsigKeyResp
	api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": "disk-check.", "algorithm": "hmac-sha256"}, &key, http.StatusCreated)
	createPrimaryZone(t, api, "tsig-user.test.", map[string]any{"update": map[string]any{"tsig_key_ids": []string{key.ID}}})

	for _, c := range []struct{ zone, backend string }{{"kek.test.", "kek"}, {"hsm.test.", "pkcs11"}} {
		_, st := createSignedZone(t, api, c.zone, c.backend)
		anchors := anchorsFor(t, c.zone, keysWith(st, "ksk", "active"))
		harness.EventuallyTrue(t, 15*time.Second, func() bool {
			return strings.Contains(harness.Delv(t, eng.DNS, anchors, strings.TrimSuffix(c.zone, "."), "www."+c.zone, "A"), "; fully validated")
		}, c.zone+" validates")
		validates(t, eng.DNS, anchors, c.zone, c.backend)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, e.pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	kekRaw, err := os.ReadFile(kekFile)
	if err != nil {
		t.Fatal(err)
	}
	kek, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(kekRaw)))
	if err != nil {
		t.Fatal(err)
	}
	var dump string
	if err := conn.QueryRow(ctx, `SELECT string_agg(k::text, E'\n') FROM dnssec_keys k`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `SELECT k.backend, k.key_ref, k.private_envelope FROM dnssec_keys k JOIN zones z ON z.id = k.zone_id WHERE z.name IN ('kek.test.', 'hsm.test.')`)
	if err != nil {
		t.Fatal(err)
	}
	var privateScalars [][]byte
	var kekKeys, hsmKeys int
	for rows.Next() {
		var backend string
		var ref, env []byte
		if err := rows.Scan(&backend, &ref, &env); err != nil {
			t.Fatal(err)
		}
		switch backend {
		case "kek":
			kekKeys++
			der := openNXE1(t, kek, "nexora/dnssec/v1:"+hex.EncodeToString(ref), env)
			priv, err := x509.ParsePKCS8PrivateKey(der)
			if err != nil {
				t.Fatalf("KEK envelope does not decrypt to PKCS#8: %v", err)
			}
			d := priv.(*ecdsa.PrivateKey).D.FillBytes(make([]byte, 32))
			privateScalars = append(privateScalars, d, der)
		case "pkcs11":
			hsmKeys++
			if env != nil {
				t.Fatal("PKCS#11 key row carries a private envelope")
			}
		}
	}
	rows.Close()
	if kekKeys != 2 || hsmKeys != 2 {
		t.Fatalf("keys per backend: kek=%d pkcs11=%d", kekKeys, hsmKeys)
	}
	for _, secret := range privateScalars {
		for _, form := range []string{string(secret), hex.EncodeToString(secret), base64.StdEncoding.EncodeToString(secret)} {
			if strings.Contains(dump, form) {
				t.Fatal("plaintext private key material found in dnssec_keys")
			}
		}
	}

	tsigSecret, err := base64.StdEncoding.DecodeString(key.Secret)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := append([][]byte{tsigSecret, []byte(key.Secret), []byte(hex.EncodeToString(tsigSecret))}, privateScalars...)
	sawSnapshot := false
	err = filepath.WalkDir(eng.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, "snapshot.binpb") {
			sawSnapshot = bytes.Contains(data, []byte("tsig-user"))
		}
		for _, f := range forbidden {
			if bytes.Contains(data, f) {
				t.Fatalf("key material found on engine disk in %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawSnapshot {
		t.Fatal("engine snapshot with the zones was not found on disk (positive check)")
	}

	t.Run("refuses secrets without key storage", func(t *testing.T) {
		bare := startAuthEnv(t, nil)
		status, err := bare.api.Do(http.MethodPost, "/tsig-keys", map[string]any{"name": "k.", "algorithm": "hmac-sha256"}, nil)
		if status != http.StatusServiceUnavailable || err == nil || !strings.Contains(err.Error(), "key_storage_unconfigured") {
			t.Fatalf("tsig key without key storage: %d %v", status, err)
		}
		z := createPrimaryZone(t, bare.api, "nokeys.test.", nil)
		status, err = bare.api.Do(http.MethodPut, "/zones/"+z.ID+"/dnssec", map[string]any{"revision": z.Revision, "enabled": true}, nil)
		if status != http.StatusServiceUnavailable || err == nil || !strings.Contains(err.Error(), "key_storage_unconfigured") {
			t.Fatalf("dnssec without key storage: %d %v", status, err)
		}
	})
}

// openNXE1 decrypts an NXE1 file-KEK envelope independently of mgmt code.
func openNXE1(t *testing.T, kek []byte, purpose string, env []byte) []byte {
	t.Helper()
	if len(env) < 101 || string(env[:4]) != "NXE1" || env[4] != 1 {
		t.Fatalf("not a file-KEK NXE1 envelope")
	}
	kb, err := aes.NewCipher(kek)
	if err != nil {
		t.Fatal(err)
	}
	kg, err := cipher.NewGCM(kb)
	if err != nil {
		t.Fatal(err)
	}
	dek, err := kg.Open(nil, env[13:25], env[25:73], []byte("NXE1-dek"))
	if err != nil {
		t.Fatalf("unwrap DEK: %v", err)
	}
	db, err := aes.NewCipher(dek)
	if err != nil {
		t.Fatal(err)
	}
	dg, err := cipher.NewGCM(db)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := dg.Open(nil, env[73:85], env[85:], []byte(purpose))
	if err != nil {
		t.Fatalf("open envelope: %v", err)
	}
	return plain
}
