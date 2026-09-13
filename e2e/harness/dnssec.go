package harness

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TrustAnchor is one static DNSKEY trust anchor.
type TrustAnchor struct {
	Flags     uint16
	Protocol  uint8
	Algorithm uint8
	PublicKey string
}

// WriteTrustAnchors writes a BIND trust-anchors file for zone and returns its path.
func WriteTrustAnchors(t *testing.T, zone string, anchors []TrustAnchor) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("trust-anchors {\n")
	for _, a := range anchors {
		fmt.Fprintf(&b, "\t%q static-key %d %d %d %q;\n", zone, a.Flags, a.Protocol, a.Algorithm, a.PublicKey)
	}
	b.WriteString("};\n")
	p := filepath.Join(t.TempDir(), "anchors.conf")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Delv validates name/qtype against server using only the given trust anchor file rooted at root.
func Delv(t *testing.T, server, anchorFile, root, name, qtype string) string {
	t.Helper()
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		t.Fatal(err)
	}
	// delv comes from $PATH and every argument is built by the calling test.
	out, err := exec.Command("delv", "-4", "@"+host, "-p", port, "-a", anchorFile, "+root="+root, name, qtype).CombinedOutput() // nosemgrep: dangerous-exec-command
	if err != nil && len(out) == 0 {
		t.Fatalf("delv: %v", err)
	}
	return string(out)
}
