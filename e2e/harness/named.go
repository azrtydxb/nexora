package harness

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Named is a running BIND 9 instance: primaries (optionally restricted to a TSIG key) and
// secondaries configured by StartNamedConfig. KeyName/KeySecretB64 are the key StartNamed drew.
type Named struct {
	Addr, KeyName, KeySecretB64 string
	Proc                        *Proc

	// zone and file are the first primary zone and its file name, used by serving and UpdateZone.
	dir, zone, file string
	cred            *syscall.Credential
}

// namedAttempts bounds the restarts of named when its pre-picked port was taken meanwhile.
const namedAttempts = 3

// NamedKey is a TSIG key named knows; SecretB64 is the base64 secret.
type NamedKey struct{ Name, Algorithm, SecretB64 string }

// NamedZone is one zone: Type "primary" (Text is the zone file; AllowTransferKey restricts
// transfers to that key, otherwise any client may transfer; AllowUpdateKey enables RFC 2136
// updates with that key; AlsoNotify is "ip:port") or "secondary" (Primary is "ip:port", KeyName
// signs the transfer).
type NamedZone struct{ Name, Type, Text, Primary, KeyName, AllowTransferKey, AllowUpdateKey, AlsoNotify string }

// NamedConfig configures StartNamedConfig.
type NamedConfig struct {
	Keys  []NamedKey
	Zones []NamedZone
}

// StartNamed starts `named` (from $PATH) as the primary for zoneName with zoneText, allowing
// transfers only with TSIG key rpz-key. (hmac-sha256, random secret).
func (e *Env) StartNamed(zoneName, zoneText string) *Named {
	t := e.T
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(secret)
	n := e.StartNamedConfig(NamedConfig{
		Keys:  []NamedKey{{Name: "rpz-key.", Algorithm: "hmac-sha256", SecretB64: b64}},
		Zones: []NamedZone{{Name: zoneName, Type: "primary", Text: zoneText, AllowTransferKey: "rpz-key."}},
	})
	n.KeyName, n.KeySecretB64 = "rpz-key.", b64
	return n
}

// StartNamedConfig starts `named` with c on a free loopback port, retrying when the port was
// taken, and waits until it serves: SOA of its first primary zone, or CHAOS version.bind. As root
// it runs as user dev, because named refuses to keep root and the directory must be writable for
// its journals.
func (e *Env) StartNamedConfig(c NamedConfig) *Named {
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
	n := &Named{dir: dir, cred: pgCredential(t)}
	t.Cleanup(func() {
		n.Stop()
		_ = os.RemoveAll(dir)
	})
	if n.cred != nil {
		if err := os.Chown(dir, int(n.cred.Uid), int(n.cred.Gid)); err != nil {
			t.Fatal(err)
		}
	}
	var body strings.Builder
	for _, k := range c.Keys {
		fmt.Fprintf(&body, "key %q { algorithm %s; secret %q; };\n", k.Name, k.Algorithm, k.SecretB64)
	}
	for _, z := range c.Zones {
		name := dns.Fqdn(z.Name)
		file := strings.TrimSuffix(name, ".") + ".db"
		switch z.Type {
		case "primary":
			n.writeFile(t, file, z.Text)
			if n.zone == "" {
				n.zone, n.file = name, file
			}
			extra := ""
			if z.AllowTransferKey != "" {
				extra += fmt.Sprintf(" allow-transfer { key %q; };", z.AllowTransferKey)
			}
			if z.AllowUpdateKey != "" {
				extra += fmt.Sprintf(" allow-update { key %q; };", z.AllowUpdateKey)
			}
			if z.AlsoNotify != "" {
				host, port, err := net.SplitHostPort(z.AlsoNotify)
				if err != nil {
					t.Fatal(err)
				}
				extra += fmt.Sprintf(" notify explicit; also-notify { %s port %s; };", host, port)
			}
			fmt.Fprintf(&body, "zone %q { type primary; file %q;%s };\n", name, filepath.Join(dir, file), extra)
		case "secondary":
			host, port, err := net.SplitHostPort(z.Primary)
			if err != nil {
				t.Fatal(err)
			}
			key := ""
			if z.KeyName != "" {
				key = fmt.Sprintf(" key %q", z.KeyName)
			}
			fmt.Fprintf(&body, "zone %q { type secondary; primaries { %s port %s%s; }; file %q; };\n", name, host, port, key, filepath.Join(dir, file))
		default:
			t.Fatalf("named zone %s: unknown type %q", name, z.Type)
		}
	}
	for attempt := 1; ; attempt++ {
		port := e.FreePort()
		conf := fmt.Sprintf(`options {
	directory %[1]q;
	listen-on port %[2]d { 127.0.0.1; };
	listen-on-v6 { none; };
	pid-file "%[1]s/named.pid";
	session-keyfile "%[1]s/session.key";
	managed-keys-directory %[1]q;
	recursion no;
	notify no;
	allow-transfer { any; };
	ixfr-from-differences yes;
	dnssec-validation no;
};
controls { };
%[3]s`, dir, port, body.String())
		n.writeFile(t, "named.conf", conf)
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
			t.Fatalf("named did not start on %s after %d attempts:\n%s", n.Addr, attempt, tail(n.Proc.LogPath, 50))
		}
	}
}

// serving waits up to 10 s for a TCP SOA query (version.bind CH TXT without a primary zone) to
// succeed; it returns false early when named
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
		if n.zone != "" {
			m.SetQuestion(n.zone, dns.TypeSOA)
		} else {
			m.SetQuestion("version.bind.", dns.TypeTXT)
			m.Question[0].Qclass = dns.ClassCHAOS
		}
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
	n.writeFile(t, n.file, zoneText)
	n.Proc.Signal(syscall.SIGHUP)
}

// Stop stops named.
func (n *Named) Stop() {
	if n.Proc != nil {
		n.Proc.Stop()
	}
}
