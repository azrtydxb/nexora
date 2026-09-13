package e2e

import "encoding/hex"

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }
