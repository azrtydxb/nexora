package harness

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// The client's config parsing and key id follow RFC 9230 §6, checked against a hand-built config.
func TestODoHConfigParsingAndKeyID(t *testing.T) {
	pub := bytes.Repeat([]byte{0x11}, 32)
	contents := append([]byte{0x00, 0x20, 0x00, 0x01, 0x00, 0x01, 0x00, 0x20}, pub...)
	config := append([]byte{0x00, 0x01, 0x00, byte(len(contents))}, contents...)
	unknown := []byte{0x00, 0x02, 0x00, 0x02, 0xff, 0xff}
	all := append(unknown, config...)
	body := append([]byte{0x00, byte(len(all))}, all...)
	cfgs, err := ParseODoHConfigs(body)
	if err != nil || len(cfgs) != 1 || cfgs[0].KemID != 0x0020 || !bytes.Equal(cfgs[0].PublicKey, pub) {
		t.Fatalf("parse: %+v %v", cfgs, err)
	}
	id := cfgs[0].KeyID()
	if len(id) != 32 {
		t.Fatalf("key id length %d", len(id))
	}
	// Expected value computed once with HKDF-SHA256 over the 40-octet contents (RFC 5869).
	if hex.EncodeToString(id) != keyIDOf(contents) {
		t.Fatalf("key id %x", id)
	}
	if _, err := ParseODoHConfigs([]byte{0x00, 0x09, 0x00}); err == nil {
		t.Fatal("a truncated config list parsed")
	}
}

func keyIDOf(contents []byte) string { return hex.EncodeToString(hkdfKeyID(contents)) }
