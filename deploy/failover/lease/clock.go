package lease

import (
	"errors"
	"strings"
	"sync"
)

// BootClock returns absolute kernel boottime, never elapsed time since creation.
// A validation/read failure latches terminal and Now returns zero, which the
// controller rejects. Err supplies diagnostics without changing Clock's API.
// This cannot establish oscillator/drain bounds or detect every VM clock fault.
type BootClock struct {
	mu              sync.Mutex
	boot, namespace string
	last            uint64
	err             error
}

func (c *BootClock) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

func zeroOffsets(s string) error {
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		p := strings.Fields(line)
		if len(p) != 3 || (p[0] != "monotonic" && p[0] != "boottime") || seen[p[0]] || p[1] != "0" || p[2] != "0" {
			return errors.New("unknown or nonzero time namespace offsets")
		}
		seen[p[0]] = true
	}
	if !seen["monotonic"] || !seen["boottime"] {
		return errors.New("missing time namespace offsets")
	}
	return nil
}
