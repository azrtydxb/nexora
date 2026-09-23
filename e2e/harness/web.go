package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RunPlaywright runs `pnpm exec playwright test <specs...>` in <repo>/web with env (plus CI=1),
// logs the output and fails the test on a non-zero exit. The coverage directory is
// env["NEXORA_E2E_COVERAGE_DIR"] when set, otherwise a new temporary directory; it is returned.
func RunPlaywright(t *testing.T, specs []string, env map[string]string) string {
	t.Helper()
	coverage := env["NEXORA_E2E_COVERAGE_DIR"]
	if coverage == "" {
		coverage = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	// Each invocation needs its own output directory: Playwright clears --output
	// at startup, so a later successful test must not erase an earlier failure.
	output, err := newPlaywrightOutput(filepath.Join(repoRoot(), "web", "test-results"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Playwright artifacts: %s", output)
	// specs come from test code; pnpm resolves from PATH like any developer invocation.
	cmd := exec.CommandContext(ctx, "pnpm", append([]string{"exec", "playwright", "test", "--output", output}, specs...)...) // nosemgrep: dangerous-exec-command
	cmd.Dir = filepath.Join(repoRoot(), "web")
	cmd.Env = append(os.Environ(), "CI=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "NEXORA_E2E_COVERAGE_DIR="+coverage)
	out, err := cmd.CombinedOutput()
	t.Logf("playwright %s:\n%s", strings.Join(specs, " "), out)
	if err != nil {
		t.Fatalf("playwright %s: %v", strings.Join(specs, " "), err)
	}
	return coverage
}

func newPlaywrightOutput(base string) (string, error) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	private, err := os.MkdirTemp(base, "run-")
	if err != nil {
		return "", err
	}
	// Playwright recreates --output with its own permissions. Keep a private
	// ancestor outside that reset so retained traces cannot expose session data.
	output := filepath.Join(private, "artifacts")
	if err := os.Mkdir(output, 0o700); err != nil {
		_ = os.Remove(private)
		return "", err
	}
	return output, nil
}
