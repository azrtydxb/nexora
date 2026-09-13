package harness

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// SoftHSMModule is the SoftHSM2 PKCS#11 module installed in the dev image.
const SoftHSMModule = "/usr/lib/softhsm/libsofthsm2.so"

// SoftHSM is one initialised SoftHSM2 token in a private token directory.
type SoftHSM struct {
	Module, Label, PinFile string
	Conf                   string // SOFTHSM2_CONF of the token directory
}

// InitSoftHSM creates a private SoftHSM2 token store for this test, points SOFTHSM2_CONF at it (for
// modules loaded in the test process; child processes need Env) and initialises a token with a
// random user PIN written to a 0600 file. The SO PIN is random and discarded.
func InitSoftHSM(t *testing.T, label string) SoftHSM {
	t.Helper()
	dir := t.TempDir()
	tokens := filepath.Join(dir, "tokens")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "softhsm2.conf")
	if err := os.WriteFile(conf, []byte("directories.tokendir = "+tokens+"\nobjectstore.backend = file\nlog.level = ERROR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOFTHSM2_CONF", conf)
	userPIN, soPIN := softHSMPIN(t, 8), softHSMPIN(t, 8)
	cmd := exec.Command("softhsm2-util", "--init-token", "--free", "--label", label, "--pin", userPIN, "--so-pin", soPIN)
	cmd.Env = append(os.Environ(), "SOFTHSM2_CONF="+conf)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("softhsm2-util --init-token: %v\n%s", err, out)
	}
	pin := filepath.Join(dir, "pin")
	if err := os.WriteFile(pin, []byte(userPIN+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return SoftHSM{Module: SoftHSMModule, Label: label, PinFile: pin, Conf: conf}
}

// Env returns the management plane environment that selects this token.
func (s SoftHSM) Env() []string {
	return []string{"SOFTHSM2_CONF=" + s.Conf, "NEXORA_PKCS11_MODULE=" + s.Module,
		"NEXORA_PKCS11_TOKEN_LABEL=" + s.Label, "NEXORA_PKCS11_PIN_FILE=" + s.PinFile}
}

func softHSMPIN(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
