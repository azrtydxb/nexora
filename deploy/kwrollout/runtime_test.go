package kwrollout

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRenderedRuntimeStages(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("Helm is required for the runtime rendering contract")
	}
	for _, target := range []string{"", "nexora-engine-a", "nexora-engine-b", "nexora-engine-c", "nexora-engine-d"} {
		stage := Stage{RollingWorkload: target}
		args := []string{"template", "nexora", "../helm/nexora", "-n", "nexora", "-f", "../kw/values-kw.yaml", "-f", "../kw/values-pairs.yaml", "--set-string", "image.tag=sha-1234567", "--set-string", "engine.pairedRollout.workload=" + target, "--api-versions", "monitoring.coreos.com/v1"}
		out, err := exec.Command("helm", args...).Output()
		if err != nil {
			t.Fatal(err)
		}
		image := "192.168.10.131/azrtydxb/nexora-engine:sha-1234567"
		if err := validateRenderedEngines(out, stage, image); err != nil {
			t.Fatal(err)
		}
		for _, broken := range []string{
			strings.ReplaceAll(string(out), "cpu: 500m", "cpu: 5"),
			strings.ReplaceAll(string(out), "master-11", "worker-21"),
			strings.ReplaceAll(string(out), "type: OnDelete", "type: RollingUpdate"),
			strings.ReplaceAll(string(out), image, "image:wrong"),
		} {
			if validateRenderedEngines([]byte(broken), stage, image) == nil {
				t.Fatal("unsafe render accepted")
			}
		}
	}
}

func TestSupportingPhasesAreInsideLockAndMonitor(t *testing.T) {
	for _, failing := range []string{"", "prepare", "finalize"} {
		var events []string
		s := fakeSteps(&events, true)
		failure := errors.New("phase failed")
		s.prepare = func(context.Context) error {
			events = append(events, "prepare")
			if failing == "prepare" {
				return failure
			}
			return nil
		}
		s.finalize = func(context.Context) error {
			events = append(events, "finalize")
			if failing == "finalize" {
				return failure
			}
			return nil
		}
		err := run(context.Background(), testPairs(), s)
		if failing == "" {
			if err != nil {
				t.Fatal(err)
			}
			if slices.Index(events, "prepare") < slices.Index(events, "health:existing") || slices.Index(events, "prepare") > slices.Index(events, "apply:frozen") || slices.Index(events, "finalize") > slices.Index(events, "unlock") || !slices.Contains(events, "health:bootstrapped") {
				t.Fatalf("phase outside guard: %v", events)
			}
		} else if !errors.Is(err, failure) || slices.Contains(events, "unlock") {
			t.Fatalf("err=%v events=%v", err, events)
		}
	}
}

func TestShellGuardRejectsLossAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var owned atomic.Bool
	owned.Store(true)
	g, err := newMutationGuard(ctx, func(context.Context) error {
		if !owned.Load() {
			return errors.New("lost")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.close()
	info, err := os.Stat(g.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket is not private")
	}
	invoke := func() error {
		cmd := exec.Command("bash", "../../scripts/kw-guard.sh")
		cmd.Env = guardEnvironment(os.Environ(), map[string]string{"NEXORA_KW_GUARD_SOCKET": g.path})
		return cmd.Run()
	}
	if err := invoke(); err != nil {
		t.Fatal(err)
	}
	owned.Store(false)
	if invoke() == nil {
		t.Fatal("lost owner authorized mutation")
	}
	owned.Store(true)
	cancel()
	if invoke() == nil {
		t.Fatal("cancelled owner authorized mutation")
	}
}

func TestSupportRefusesMissingSecretsBeforeMutations(t *testing.T) {
	guard, err := newMutationGuard(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer guard.close()
	for _, secret := range []string{"nexora-ca", "nexora-kek", "nexora-demo-tsig", "nexora-dns-tls", "nexora-join-token"} {
		t.Run(secret, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "mutations")
			stub := "#!/bin/sh\ncase \"$*\" in\n*'get secret " + secret + "') exit 1;;\n*'get secret '*) exit 0;;\nesac\necho unexpected >> '" + log + "'\nexit 1\n"
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "../../scripts/kw-deploy.sh", "--skip-build", "--tag", "sha-1234567")
			cmd.Env = guardEnvironment(os.Environ(), map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"), "NEXORA_KW_DEPLOY_PHASE": "support", "NEXORA_KW_GUARD_SOCKET": guard.path})
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "required existing Secret "+secret+" unavailable") {
				t.Fatalf("missing secret was not rejected: %s (%v)", out, err)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("mutation attempted despite missing persistent secret")
			}
		})
	}
}

func TestShellPhasesRefuseMissingGuardBeforeCommands(t *testing.T) {
	for _, entry := range []struct{ script, phase string }{{"../../scripts/kw-deploy.sh", "support"}, {"../../scripts/kw-deploy.sh", "bootstrap"}, {"../kw/bootstrap.sh", "bootstrap"}} {
		dir := t.TempDir()
		log := filepath.Join(dir, "calls")
		if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\necho called >> '"+log+"'\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", entry.script, "--skip-build", "--tag", "sha-1234567")
		cmd.Env = guardEnvironment(os.Environ(), map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"), "NEXORA_KW_DEPLOY_PHASE": entry.phase, "NEXORA_KW_GUARD_SOCKET": ""})
		if cmd.Run() == nil {
			t.Fatal("unguarded script succeeded")
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatal("unguarded script called kubectl")
		}
	}
}

func TestSupportClickHouseCredentialsFailClosed(t *testing.T) {
	guard, err := newMutationGuard(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer guard.close()
	for _, mode := range []string{"lookup-error", "missing-existing-secret", "orphaned-data", "pvc-lookup-error"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "unexpected")
			lookup, workload, pvc := "exit 0", "exit 0", "exit 0"
			switch mode {
			case "lookup-error":
				lookup = "exit 1"
			case "missing-existing-secret":
				workload = "echo statefulset.apps/clickhouse; exit 0"
			case "orphaned-data":
				pvc = "echo persistentvolumeclaim/data-clickhouse-0; exit 0"
			case "pvc-lookup-error":
				pvc = "exit 1"
			}
			stub := "#!/bin/sh\ncase \"$*\" in\n*'get secret nexora-clickhouse '*) " + lookup + ";;\n*'get secret '*) exit 0;;\n*'namespace.yaml') exit 0;;\n*'get statefulset clickhouse '*) " + workload + ";;\n*'get pvc data-clickhouse-0 '*) " + pvc + ";;\nesac\necho unexpected >> '" + log + "'\nexit 1\n"
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "../../scripts/kw-deploy.sh", "--skip-build", "--tag", "sha-1234567")
			cmd.Env = guardEnvironment(os.Environ(), map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"), "NEXORA_KW_DEPLOY_PHASE": "support", "NEXORA_KW_GUARD_SOCKET": guard.path})
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("unsafe support phase succeeded: %s", out)
			}
			if (mode == "missing-existing-secret" || mode == "orphaned-data") && !strings.Contains(string(out), "restore it before upgrading") {
				t.Fatalf("missing recovery instruction: %s", out)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("mutation attempted after failed ClickHouse credential check")
			}
		})
	}
}
