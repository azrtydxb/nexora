package e2e

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestMgmtCLIFleet(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	run := func(args ...string) (string, string, error) {
		cmd := exec.Command(env.Bin("nexora-mgmt"), args...) // nosemgrep: dangerous-exec-command
		cmd.Env = append(os.Environ(), "NEXORA_DATABASE_URL="+pg.URL, "NEXORA_CA_CERT_FILE="+ca.CertFile)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return strings.TrimSpace(out.String()), errb.String(), err
	}

	gid, stderr, err := run("engine-group", "create", "--name", "edge-cli", "--description", "from cli")
	if err != nil || !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(gid) {
		t.Fatalf("engine-group create: %q %q %v", gid, stderr, err)
	}
	if g := api.EngineGroup(gid); g.Name != "edge-cli" || g.Description != "from cli" {
		t.Fatalf("engine group via API: %+v", g)
	}
	if again, _, err := run("engine-group", "create", "--name", "edge-cli", "--if-missing"); err != nil || again != gid {
		t.Fatalf("engine-group create --if-missing: %q %v, want %s", again, err, gid)
	}
	if _, stderr, err := run("engine-group", "create", "--name", "edge-cli"); err == nil || !strings.Contains(stderr, `engine group "edge-cli" already exists`) {
		t.Fatalf("duplicate engine-group create: %q %v", stderr, err)
	}

	token, stderr, err := run("join-token", "create", "--engine-group", "edge-cli", "--ttl", "10m", "--max-uses", "3", "--label", "site=lab")
	if err != nil || !regexp.MustCompile(`^nxj1\.[A-Z2-7]+\.[0-9a-f]{64}$`).MatchString(token) {
		t.Fatalf("join-token create: %q %q %v", token, stderr, err)
	}
	if _, stderr, err := run("join-token", "create", "--engine-group", "missing"); err == nil || !strings.Contains(stderr, `engine group "missing" not found`) {
		t.Fatalf("unknown engine group: %q %v", stderr, err)
	}
	env.StartManagedEngine("cli-1", []string{mg.GRPCURL}, token)
	e := api.WaitEngine("cli-1", 15*time.Second, func(v harness.EngineView) bool { return v.Connected })
	if e.EngineGroupID != gid || e.Labels["site"] != "lab" {
		t.Fatalf("engine enrolled with the CLI token: %+v", e)
	}
	var listed []map[string]any
	api.Must(http.MethodGet, "/join-tokens", nil, &listed, http.StatusOK)
	if len(listed) != 1 || listed[0]["max_uses"] != float64(3) || listed[0]["uses"] != float64(1) {
		t.Fatalf("listed join tokens %+v", listed)
	}

	before, _ := os.ReadFile(ca.CertFile)
	if out, stderr, err := run("ca", "init", "--out", ca.Dir, "--if-missing"); err != nil || !strings.HasPrefix(out, "ca fingerprint: ") {
		t.Fatalf("ca init --if-missing on an existing CA: %q %q %v", out, stderr, err)
	}
	after, _ := os.ReadFile(ca.CertFile)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("ca init --if-missing replaced an existing CA")
	}
	if _, stderr, err := run("ca", "init", "--out", ca.Dir); err == nil || !strings.Contains(stderr, "refusing to overwrite") {
		t.Fatalf("ca init over an existing CA: %q %v", stderr, err)
	}
}
