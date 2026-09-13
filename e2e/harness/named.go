package harness

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"

	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"text/template"
	"time"

	"github.com/miekg/dns"
)

// Named is a running BIND 9 primary serving one zone over AXFR/IXFR to holders of a TSIG key.
type Named struct {
	Addr, KeyName, KeySecretB64 string
	Proc                        *Proc

	dir, zone string
	cred      *syscall.Credential
}

var namedConf = template.Must(template.New("named.conf").Parse(`options {
	directory "{{.Dir}}";
	listen-on port {{.Port}} { 127.0.0.1; };
	listen-on-v6 { none; };
	pid-file "{{.Dir}}/named.pid";
	session-keyfile "{{.Dir}}/session.key";
	managed-keys-directory "{{.Dir}}";
	recursion no;
	notify no;
	allow-transfer { key "{{.KeyName}}"; };
	ixfr-from-differences yes;
	dnssec-validation no;
};
controls { };
key "{{.KeyName}}" {
	algorithm hmac-sha256;
	secret "{{.Secret}}";
};
zone "{{.Zone}}" {
	type primary;
	file "{{.Dir}}/zone.db";
};
`))

// namedAttempts bounds the restarts of named when its pre-picked port was taken meanwhile.
const namedAttempts = 3

// StartNamed starts `named` (from $PATH) as the primary for zoneName with zoneText, allowing
// transfers only with TSIG key rpz-key. (hmac-sha256, random secret). As root it runs as user
// dev, because named refuses to keep root and the directory must be writable for its journal.
func (e *Env) StartNamed(zoneName, zoneText string) *Named {
	t := e.T
	t.Helper()
	// The directory lives outside t.TempDir(), whose root-owned 0700 parents the unprivileged
	// named user cannot enter.
	dir, err := os.MkdirTemp("", "nexora-named-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	n := &Named{KeyName: "rpz-key.", KeySecretB64: base64.StdEncoding.EncodeToString(secret),
		dir: dir, zone: dns.Fqdn(zoneName), cred: pgCredential(t)}
	t.Cleanup(func() {
		n.Stop()
		_ = os.RemoveAll(dir)
	})
	if n.cred != nil {
		if err := os.Chown(dir, int(n.cred.Uid), int(n.cred.Gid)); err != nil {
			t.Fatal(err)
		}
	}
	n.writeFile(t, "zone.db", zoneText)
	for attempt := 1; ; attempt++ {
		port := e.FreePort()
		var conf bytes.Buffer
		if err := namedConf.Execute(&conf, map[string]any{"Dir": dir, "Port": port, "KeyName": n.KeyName,
			"Secret": n.KeySecretB64, "Zone": n.zone}); err != nil {
			t.Fatal(err)
		}
		n.writeFile(t, "named.conf", conf.String())
		args := []string{"-g", "-c", filepath.Join(dir, "named.conf")}
		if n.cred != nil {
			args = append(args, "-u", "dev")
		}
		n.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		n.Proc = e.Start("named", args, nil)
		if n.serving() {
			return n
		}
		n.Proc.Stop()
		if attempt == namedAttempts {
			t.Fatalf("named did not serve %s on %s after %d attempts:\n%s", n.zone, n.Addr, attempt, tail(n.Proc.LogPath, 50))
		}
	}
}

// serving waits up to 10 s for a TCP SOA query to succeed; it returns false early when named
// exits (typically because the port was taken between FreePort and the bind).
func (n *Named) serving() bool {
	deadline := time.Now().Add(10 * time.Second)
	c := &dns.Client{Net: "tcp", Timeout: time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-n.Proc.done:
			return false
		default:
		}
		m := new(dns.Msg)
		m.SetQuestion(n.zone, dns.TypeSOA)
		if r, _, err := c.Exchange(m, n.Addr); err == nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (n *Named) writeFile(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(n.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if n.cred != nil {
		if err := os.Chown(path, int(n.cred.Uid), int(n.cred.Gid)); err != nil {
			t.Fatal(err)
		}
	}
}

// UpdateZone rewrites the zone file and sends SIGHUP; named reloads it and, with
// ixfr-from-differences, journals the change for IXFR.
func (n *Named) UpdateZone(t *testing.T, zoneText string) {
	t.Helper()
	n.writeFile(t, "zone.db", zoneText)
	n.Proc.Signal(syscall.SIGHUP)
}

// Stop stops named.
func (n *Named) Stop() {
	if n.Proc != nil {
		n.Proc.Stop()
	}
}
