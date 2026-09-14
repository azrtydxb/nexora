package blocklist_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"slices"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/blocklist"
)

// UT1Archive builds a blacklists.tar.gz with the given member contents.
func UT1Archive(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadArchiveMember(t *testing.T) {
	archive := UT1Archive(t, map[string]string{
		"blacklists/README":           "UT1",
		"blacklists/gambling/domains": "casino.ut1.test\nbet.ut1.test\n",
		"blacklists/adult/domains":    "adult.ut1.test\n",
		"blacklists/gambling/urls":    "casino.ut1.test/path\n",
	})
	domains, stats, err := blocklist.ReadArchiveMember(bytes.NewReader(archive), "blacklists/gambling/domains")
	if err != nil || stats.Entries != 2 || !slices.Contains(domains, "casino.ut1.test") || slices.Contains(domains, "adult.ut1.test") {
		t.Fatalf("member: %v %+v %v", domains, stats, err)
	}
	if _, _, err := blocklist.ReadArchiveMember(bytes.NewReader(archive), "blacklists/dating/domains"); err == nil || err.Error() != "archive member blacklists/dating/domains not found" {
		t.Fatalf("missing member -> %v", err)
	}
	if _, _, err := blocklist.ReadArchiveMember(strings.NewReader("not a gzip stream"), "blacklists/gambling/domains"); err == nil || !strings.HasPrefix(err.Error(), "read archive: ") {
		t.Fatalf("corrupt archive -> %v", err)
	}
}
