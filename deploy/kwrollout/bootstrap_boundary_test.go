package kwrollout

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// No listener permissions needed: run the production Bash/JQ wait with a fake
// curl executable. Transport and the real guard are covered separately by the
// full bootstrap TLS test; this harness does not stand in for that integration.
func TestBootstrapBoundaryCommands(t *testing.T) {
	for _, scenario := range []string{"lag", "applied-lag", "ready", "utc", "invalid-time", "missing-time", "timeout", "stale", "rejected", "persist", "future", "ahead", "target-moved", "identity", "disconnected", "malformed", "dns-failure", "redirect", "ownership", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			root, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			engines, _ := healthyManagementFixture()
			for i := range engines {
				engines[i].AppliedVersion = 1234
				engines[i].TargetVersion = 1234
			}
			encode := func(name string) {
				b, err := json.Marshal(engines)
				if err != nil {
					t.Fatal(err)
				}
				write(name, string(b))
			}
			encode("pin.json")
			encode("ready.json")
			switch scenario {
			case "lag", "timeout":
				engines[0].AppliedVersion = 1233
				engines[0].TargetVersion = 1233
			case "applied-lag":
				engines[0].AppliedVersion = 1233
				engines[0].Status = "behind"
			case "utc":
				now := time.Now().UTC().Truncate(time.Second)
				engines[0].LastSeenAt = &now
			case "missing-time":
				engines[0].LastSeenAt = nil
			case "stale":
				old := time.Now().Add(-time.Minute)
				engines[0].LastSeenAt = &old
			case "future":
				future := time.Now().Add(time.Minute)
				engines[0].LastSeenAt = &future
			case "rejected":
				engines[0].RejectedReason = "invalid"
				engines[0].Status = "rejected"
			case "persist":
				engines[0].PersistError = "disk full"
			case "ahead":
				engines[0].TargetVersion++
			case "identity":
				engines[0].ID = "replacement"
			case "disconnected":
				engines[0].Connected = false
			}
			encode("first.json")
			if scenario == "invalid-time" {
				b, _ := os.ReadFile(filepath.Join(dir, "first.json"))
				write("first.json", strings.Replace(string(b), engines[0].LastSeenAt.Format(time.RFC3339Nano), "invalid-time", 1))
			}
			if scenario == "malformed" {
				write("first.json", "not JSON")
			}
			write("curl", `#!/usr/bin/env bash
set -eu
last="${!#}"
exec 3>&1
exec >"$tmp/bootstrap-response.json"
case "$last" in
*/engines)
 n=$(cat "$tmp/reads" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" >"$tmp/reads"
 if [ "$scenario" = dns-failure ]; then exit 6; fi
 if [ "$n" -eq 1 ] || [ "$scenario" = timeout ]; then cat "$tmp/first.json"; else cat "$tmp/ready.json"; fi ;;
*'config-versions?limit=1')
 n=$(cat "$tmp/versions" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" >"$tmp/versions"
 if [ "$scenario" = target-moved ] && [ "$n" -gt 1 ]; then echo '[{"version":1235}]'; else echo '[{"version":1234}]'; fi ;;
*) echo 'unexpected request' >&2; exit 9 ;;
esac
if [ "$scenario" = redirect ] && [ -f "$tmp/reads" ]; then echo 302 >&3; else echo 200 >&3; fi
`)
			write("run.sh", `#!/usr/bin/env bash
set -euo pipefail
api=https://fixture.invalid
jar="$tmp/cookies"
guard() {
 if [ -f "$tmp/reads" ]; then
  case "$scenario" in ownership|cancel) echo 'guard refused' >&2; return 1 ;; esac
 fi
}
call() { cat "$tmp/pin.json"; }
source "$root/deploy/kwrollout/bootstrap-convergence.sh"
bootstrap_pin_engines
bootstrap_wait_convergence
`)
			cmd := exec.Command("bash", filepath.Join(dir, "run.sh"))
			budget := "10"
			if scenario == "timeout" {
				budget = "3"
			}
			cmd.Env = guardEnvironment(os.Environ(), map[string]string{"tmp": dir, "root": root, "scenario": scenario, "PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"), "NEXORA_KW_BOOTSTRAP_WAIT_SECONDS": budget})
			out, err := cmd.CombinedOutput()
			wantOK := scenario == "lag" || scenario == "applied-lag" || scenario == "ready" || scenario == "utc"
			if (err == nil) != wantOK {
				t.Fatalf("error=%v output=%s", err, out)
			}
			reads, _ := os.ReadFile(filepath.Join(dir, "reads"))
			if scenario == "lag" || scenario == "applied-lag" {
				if strings.TrimSpace(string(reads)) != "2" {
					t.Fatalf("did not poll exactly once: %s", reads)
				}
			}
			if !wantOK && scenario != "timeout" && strings.TrimSpace(string(reads)) != "1" {
				t.Fatalf("retried permanent failure: %s output=%s", reads, out)
			}
			if strings.Contains(string(out), "compile error") {
				t.Fatalf("invalid JQ program: %s", out)
			}
			if scenario == "target-moved" && !strings.Contains(string(out), "target moved") {
				t.Fatalf("wrong failure: %s", out)
			}
			if (scenario == "ownership" || scenario == "cancel") && !strings.Contains(string(out), "guard refused") {
				t.Fatalf("wrong failure: %s", out)
			}
			if scenario == "timeout" && !strings.Contains(string(out), "timed out") {
				t.Fatalf("wrong failure: %s", out)
			}
		})
	}
}
