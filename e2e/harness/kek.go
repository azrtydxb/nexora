package harness

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// WriteKEK writes a fresh key-encryption key (base64 of 32 random bytes and a newline) to a 0600
// file in the test's temporary directory and returns its path, for NEXORA_KEK_FILE.
func WriteKEK(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
