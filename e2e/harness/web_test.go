package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlaywrightArtifactDirectoriesAreIndependent(t *testing.T) {
	base := filepath.Join(t.TempDir(), "test-results")
	first, err := newPlaywrightOutput(base)
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(first, "trace.zip")
	if err := os.WriteFile(trace, []byte("preserved failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := newPlaywrightOutput(base)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || filepath.Dir(filepath.Dir(first)) != base || filepath.Dir(filepath.Dir(second)) != base {
		t.Fatalf("non-isolated artifact paths: %q, %q", first, second)
	}
	info, err := os.Stat(second)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("artifact directory must be private: %v, %v", info, err)
	}
	// Playwright clears its own output before each invocation.
	if err := os.RemoveAll(second); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	ancestor, err := os.Stat(filepath.Dir(second))
	if err != nil || ancestor.Mode().Perm() != 0o700 {
		t.Fatalf("reset exposed retained session artifacts: %v, %v", ancestor, err)
	}
	data, err := os.ReadFile(trace)
	if err != nil || string(data) != "preserved failure" {
		t.Fatalf("later invocation lost earlier evidence: %q, %v", data, err)
	}
}

func TestPlaywrightArtifactDirectoryFailure(t *testing.T) {
	base := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(base, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newPlaywrightOutput(base); err == nil {
		t.Fatal("invalid output directory accepted")
	}
}
