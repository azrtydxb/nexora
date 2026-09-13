package e2e

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

const rpzFileZone = `$TTL 60
@ SOA ns.rpz.file.test. h.rpz.file.test. 1 60 60 86400 60
@ NS ns.rpz.file.test.
www.plain.test CNAME .
mail.plain.test A 10.9.9.9
pass.plain.test CNAME rpz-passthru.
32.66.2.0.192.rpz-ip CNAME *.
32.2.0.0.127.rpz-client-ip CNAME rpz-tcp-only.
`

func rpzAxfrZone(serial int, extra string) string {
	return fmt.Sprintf("$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. %d 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\npass.plain.test CNAME .\nblocked-axfr.plain.test A 10.8.8.8\nns2.plain.test.rpz-nsdname CNAME .\n%s", serial, extra)
}

type rpzZone struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Status   []struct {
		Serial    int64  `json:"serial"`
		LastError string `json:"last_error"`
	} `json:"status"`
}

func TestRPZPolicy(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS

	// positive path before any policy exists
	wantA(t, query(t, addr, "www.plain.test", dns.TypeA, qopt{}), "192.0.2.12")
	wantA(t, query(t, addr, "www.glueless.test", dns.TypeA, qopt{}), "192.0.2.20")
	wantA(t, query(t, addr, "later.plain.test", dns.TypeA, qopt{}), "192.0.2.15")
	wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2"}), "192.0.2.10")

	named := r.env.StartNamed("rpz.axfr.test.", rpzAxfrZone(1, ""))

	var file rpzZone
	r.api.Must("POST", "/rpz-zones", map[string]any{"name": "rpz.file.test.", "source_type": "file", "policy_override": "given", "min_refresh_seconds": 60}, &file, 201)
	r.api.Must("PUT", "/rpz-zones/"+file.ID+"/file", map[string]any{"content": rpzFileZone, "revision": file.Revision}, &file, 200)
	var axfr rpzZone
	r.api.Must("POST", "/rpz-zones", map[string]any{"name": "rpz.axfr.test.", "source_type": "transfer", "primary": named.Addr, "tsig_key_name": named.KeyName, "tsig_algorithm": "hmac-sha256", "tsig_secret": named.KeySecretB64, "policy_override": "given", "min_refresh_seconds": 1}, &axfr, 201)
	waitApplied(t, r.api)
	harness.Eventually(t, 20*time.Second, func() error {
		var z rpzZone
		if _, err := r.api.Do("GET", "/rpz-zones/"+axfr.ID, nil, &z); err != nil {
			return err
		}
		if len(z.Status) != 1 || z.Status[0].Serial != 1 {
			return fmt.Errorf("status %+v", z.Status)
		}
		return nil
	})

	t.Run("tsig secret is stored sealed and never in a snapshot", func(t *testing.T) {
		secret, err := base64.StdEncoding.DecodeString(named.KeySecretB64)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, r.pg.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(context.WithoutCancel(ctx))
		var rows string
		if err := conn.QueryRow(ctx, "select string_agg(t::text, E'\\n') from rpz_zones t").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rows, "rpz.axfr.test.") {
			t.Fatalf("rpz_zones dump lacks the transfer zone: %s", rows)
		}
		if strings.Contains(rows, named.KeySecretB64) || strings.Contains(rows, hex.EncodeToString(secret)) {
			t.Fatal("plaintext TSIG secret stored in rpz_zones")
		}
		// positive first: the stored snapshots do carry the transfer zone and its key name, so the
		// byte search below looks at real snapshot content
		var carried bool
		if err := conn.QueryRow(ctx, "select coalesce(bool_or(position($1::bytea in snapshot) > 0 and position($2::bytea in snapshot) > 0), false) from group_snapshots", []byte("rpz.axfr.test."), []byte(named.KeyName)).Scan(&carried); err != nil {
			t.Fatal(err)
		}
		if !carried {
			t.Fatal("no stored config version carries the transfer zone and its TSIG key name")
		}
		var leaked bool
		if err := conn.QueryRow(ctx, "select coalesce(bool_or(position($1::bytea in snapshot) > 0), false) from group_snapshots", secret).Scan(&leaked); err != nil {
			t.Fatal(err)
		}
		if leaked {
			t.Fatal("TSIG secret found in a stored config version")
		}
	})
	t.Run("file zone qname NXDOMAIN with EDE", func(t *testing.T) {
		m := query(t, addr, "www.plain.test", dns.TypeA, qopt{})
		if m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s, want NXDOMAIN", dns.RcodeToString[m.Rcode])
		}
		if code, ok := edeCode(m); !ok || code != 15 {
			t.Fatalf("EDE = %d %v, want 15", code, ok)
		}
	})
	t.Run("file zone local data", func(t *testing.T) {
		wantA(t, query(t, addr, "mail.plain.test", dns.TypeA, qopt{}), "10.9.9.9")
	})
	t.Run("zone order: passthru in first zone beats NXDOMAIN in second", func(t *testing.T) {
		wantA(t, query(t, addr, "pass.plain.test", dns.TypeA, qopt{}), "192.0.2.14")
	})
	t.Run("response IP trigger NODATA", func(t *testing.T) {
		m := query(t, addr, "ip.plain.test", dns.TypeA, qopt{})
		if m.Rcode != dns.RcodeSuccess || len(aValues(m)) != 0 {
			t.Fatalf("rcode=%s answers=%v, want NODATA", dns.RcodeToString[m.Rcode], aValues(m))
		}
	})
	t.Run("axfr zone local data", func(t *testing.T) {
		wantA(t, query(t, addr, "blocked-axfr.plain.test", dns.TypeA, qopt{}), "10.8.8.8")
	})
	t.Run("axfr zone NSDNAME trigger overrides a previously cached answer", func(t *testing.T) {
		if m := query(t, addr, "www.glueless.test", dns.TypeA, qopt{}); m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s, want NXDOMAIN", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("client-ip trigger forces TCP", func(t *testing.T) {
		udp := query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2"})
		if !udp.Truncated || len(udp.Answer) != 0 {
			t.Fatalf("UDP from 127.0.0.2: tc=%v answers=%d", udp.Truncated, len(udp.Answer))
		}
		wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2", TCP: true}), "192.0.2.10")
	})
	t.Run("incremental transfer picks up a change", func(t *testing.T) {
		named.UpdateZone(t, rpzAxfrZone(2, "later.plain.test A 10.7.7.7\n"))
		harness.Eventually(t, 20*time.Second, func() error {
			m, err := queryErr(addr, "later.plain.test", dns.TypeA, qopt{})
			if err != nil {
				return err
			}
			if v := aValues(m); len(v) != 1 || v[0] != "10.7.7.7" {
				return fmt.Errorf("answers %v", v)
			}
			return nil
		})
	})
	t.Run("failed refresh keeps last good zone and reports error", func(t *testing.T) {
		named.Stop()
		r.api.Must("POST", "/rpz-zones/"+axfr.ID+"/refresh", map[string]any{}, nil, 202)
		harness.Eventually(t, 30*time.Second, func() error {
			var z rpzZone
			if _, err := r.api.Do("GET", "/rpz-zones/"+axfr.ID, nil, &z); err != nil {
				return err
			}
			if len(z.Status) != 1 || z.Status[0].LastError == "" {
				return fmt.Errorf("status %+v", z.Status)
			}
			return nil
		})
		wantA(t, query(t, addr, "later.plain.test", dns.TypeA, qopt{}), "10.7.7.7")
		wantA(t, query(t, addr, "mail.plain.test", dns.TypeA, qopt{}), "10.9.9.9")
	})
}
