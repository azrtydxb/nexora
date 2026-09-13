// Package harness starts real processes and services for Nexora's Go tests.
package harness

import (
	"testing"
	"time"
)

// Env is one test's harness environment.
type Env struct {
	T   *testing.T
	Dir string
}

// New creates an environment rooted in a fresh temporary directory.
func New(t *testing.T) *Env {
	t.Helper()
	return &Env{T: t, Dir: t.TempDir()}
}

// Eventually retries cond every 100 ms until it returns nil, failing the test with the last
// error when timeout elapses.
func Eventually(t *testing.T, timeout time.Duration, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := cond()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s: %v", timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
