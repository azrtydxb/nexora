package harness

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHarnessStartsClickHouseAndLoki(t *testing.T) {
	missingBinary := func(tb *Env) { tb.StartClickHouse() }
	if os.Getenv(captureFatalEnv) == t.Name() {
		captureFatal(t, missingBinary) // the child: runs the function and never returns
	}
	// The dev image carries both; a runner image that predates them skips rather than fails.
	SkipWithoutBin(t, "clickhouse", "loki")
	env := New(t)
	ch := env.StartClickHouse()
	pw, _ := os.ReadFile(ch.ReaderPasswordFile)
	req, _ := http.NewRequest(http.MethodPost, ch.HTTPURL+"/?query=SELECT%201", nil)
	req.Header.Set("X-ClickHouse-User", ch.ReaderUser)
	req.Header.Set("X-ClickHouse-Key", strings.TrimSpace(string(pw)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("SELECT 1 as reader: %v %v", resp, err)
	}
	lk := env.StartLoki(LokiOptions{})
	r, err := http.Get(lk.URL + "/ready")
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("loki ready: %v %v", r, err)
	}
	t.Setenv("PATH", t.TempDir())
	msg := captureFatal(t, missingBinary)
	if !strings.Contains(msg, "clickhouse not found: rebuild the toolbox image") {
		t.Fatalf("missing binary message: %q", msg)
	}
}

// captureFatalEnv names the test a re-executed test binary runs captureFatal's function in.
const captureFatalEnv = "NEXORA_HARNESS_CAPTURE_FATAL"

// captureFatal returns the output of fn failing its test. Env.T is a *testing.T, which cannot be
// replaced by a recording testing.TB, so the test binary re-executes itself running only t's test
// with captureFatalEnv set (and the current environment, so a t.Setenv applies); in that child
// captureFatal runs fn with a fresh Env and never returns. The calling test must call captureFatal
// before starting anything when captureFatalEnv names it.
func captureFatal(t *testing.T, fn func(tb *Env)) string {
	t.Helper()
	if os.Getenv(captureFatalEnv) == t.Name() {
		fn(New(t))
		t.Fatal("captureFatal: the function returned without failing the test")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), captureFatalEnv+"="+t.Name())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("captureFatal: the child test passed:\n%s", out)
	}
	return string(out)
}
