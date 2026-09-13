package e2e

import (
	"encoding/hex"
	"fmt"
	"os"
	"testing"
)

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }

func TestMain(m *testing.M) {
	if os.Getenv("NEXORA_E2E_BIN_DIR") == "" {
		if _, err := os.Stat("../bin/nexora-engine"); err != nil {
			fmt.Fprintln(os.Stderr, "e2e: no binaries; run `make e2e-build` (or `make e2e`)")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}
