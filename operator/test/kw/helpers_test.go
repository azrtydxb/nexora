//go:build kwe2e

package kw_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
)

const (
	mgmtBase     = "http://nexora-optest-mgmt.nexora-optest.svc:8080/api/v1"
	operatorTok  = "nexora-optest-operator-token"
	engineLabel  = "app.kubernetes.io/name"
	engineName   = "nexora-engine"
	pollInterval = 2 * time.Second
)

func kubeContext() string {
	if c := os.Getenv("NEXORA_KW_CONTEXT"); c != "" {
		return c
	}
	return "kw"
}

func namespace() string { return os.Getenv("NEXORA_OPTEST_NAMESPACE") }

// kubectl runs kubectl in the test namespace and fails the test on error.
func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := kubectlErr("", args...)
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func kubectlErr(stdin string, args ...string) (string, error) {
	cmd := exec.Command("kubectl", append([]string{"--context", kubeContext(), "-n", namespace()}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out) + stderr.String(), err
	}
	return string(out), nil
}

// probe runs script with sh in the probe pod, stdin attached, and fails the test on error.
func probe(t *testing.T, stdin, script string) string {
	t.Helper()
	out, err := probeErr(stdin, script)
	if err != nil {
		t.Fatalf("probe %q: %v\n%s", script, err, out)
	}
	return out
}

// probeErr is probe without the test, safe from other goroutines.
func probeErr(stdin, script string) (string, error) {
	return kubectlErr(stdin, "exec", "-i", "probe", "--", "sh", "-c", script)
}

// apiCall calls the management API from the probe with the operator token (stdin, never argv) and
// decodes the response into out when out is non-nil. It returns the HTTP status.
func apiCall(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	c := newClient(t)
	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace(), Name: operatorTok}, &sec); err != nil {
		t.Fatalf("operator token Secret: %v", err)
	}
	stdin := string(sec.Data["token"]) + "\n"
	data := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		stdin += string(b)
		data = " --data-binary @-"
	}
	script := fmt.Sprintf(`read -r T; o=$(mktemp); curl -sS -o "$o" -w '%%{http_code}\n' -H "Authorization: Bearer $T" -H 'Content-Type: application/json' -X %s%s '%s%s'; cat "$o"; rm -f "$o"`,
		method, data, mgmtBase, path)
	res, err := probeErr(stdin, script)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", method, path, err, res)
	}
	code, rest, _ := strings.Cut(res, "\n")
	status, err := strconv.Atoi(strings.TrimSpace(code))
	if err != nil {
		t.Fatalf("%s %s: no status in %q", method, path, res)
	}
	if out != nil && status < 300 {
		if err := json.Unmarshal([]byte(rest), out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, rest, err)
		}
	}
	return status
}

var clientCache client.Client

// newClient returns a controller-runtime client for the kw context with the v1alpha1 scheme.
func newClient(t *testing.T) client.Client {
	t.Helper()
	if clientCache != nil {
		return clientCache
	}
	// API warnings (CNPG's in-tree Barman deprecation) go nowhere instead of a missing-logger stack trace.
	logf.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	cfg, err := config.GetConfigWithContext(kubeContext())
	if err != nil {
		t.Fatalf("kube config for context %s: %v", kubeContext(), err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	clientCache = c
	return c
}

// waitFor polls cond until it returns true, failing the test after timeout with the last error.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for {
		ok, err := cond()
		if ok {
			return
		}
		if err != nil {
			last = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s (last error: %v)", timeout, what, last)
		}
		time.Sleep(pollInterval)
	}
}

func condTrue(inst *v1alpha1.NexoraInstallation, typ string) bool {
	return apimeta.IsStatusConditionTrue(inst.Status.Conditions, typ)
}

// loadInstallation reads testdata/installation.yaml with the tag, nodes and S3 path of the run.
func loadInstallation(t *testing.T) *v1alpha1.NexoraInstallation {
	t.Helper()
	raw, err := os.ReadFile("testdata/installation.yaml")
	if err != nil {
		t.Fatal(err)
	}
	nodes := strings.Split(os.Getenv("NEXORA_OPTEST_NODES"), ",")
	if len(nodes) != 3 {
		t.Fatalf("NEXORA_OPTEST_NODES must name three nodes, got %q", os.Getenv("NEXORA_OPTEST_NODES"))
	}
	tag, s3 := os.Getenv("NEXORA_OPTEST_TAG"), os.Getenv("NEXORA_OPTEST_S3_PATH")
	if tag == "" || !strings.HasPrefix(s3, "s3://nexora-optest/") {
		t.Fatalf("NEXORA_OPTEST_TAG %q and NEXORA_OPTEST_S3_PATH %q must be set", tag, s3)
	}
	text := strings.NewReplacer("TAG", tag, "NODE1", nodes[0], "NODE2", nodes[1], "NODE3", nodes[2], "S3_PATH", s3).Replace(string(raw))
	var inst v1alpha1.NexoraInstallation
	if err := yaml.UnmarshalStrict([]byte(text), &inst); err != nil {
		t.Fatalf("installation manifest: %v", err)
	}
	inst.Namespace = namespace()
	return &inst
}

// writeTemp writes obj as YAML to a file in the test's temp directory and returns its path.
func writeTemp(t *testing.T, obj any) string {
	t.Helper()
	b, err := yaml.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "object.yaml")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// serviceIP returns the cluster IP of a ClusterIP Service.
func serviceIP(t *testing.T, c client.Client, ns, name string) string {
	t.Helper()
	var svc corev1.Service
	waitFor(t, 2*time.Minute, "Service "+name, func() (bool, error) {
		return c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &svc) == nil, nil
	})
	if svc.Spec.Type != corev1.ServiceTypeClusterIP || svc.Spec.ClusterIP == "" {
		t.Fatalf("Service %s is %s with cluster IP %q; the e2e uses ClusterIP only", name, svc.Spec.Type, svc.Spec.ClusterIP)
	}
	return svc.Spec.ClusterIP
}

// enginePodUIDs is the set of engine pod UIDs.
func enginePodUIDs(t *testing.T, c client.Client, ns string) map[types.UID]bool {
	t.Helper()
	var pods corev1.PodList
	if err := c.List(context.Background(), &pods, client.InNamespace(ns), client.MatchingLabels{engineLabel: engineName}); err != nil {
		t.Fatal(err)
	}
	out := map[types.UID]bool{}
	for _, p := range pods.Items {
		out[p.UID] = true
	}
	return out
}

func engineDaemonSets(t *testing.T, c client.Client, ns string) []appsv1.DaemonSet {
	t.Helper()
	var list appsv1.DaemonSetList
	if err := c.List(context.Background(), &list, client.InNamespace(ns), client.MatchingLabels{engineLabel: engineName}); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

// enginesReady reports whether every engine DaemonSet finished its rollout with every pod ready.
func enginesReady(t *testing.T, c client.Client, ns string) bool {
	t.Helper()
	list := engineDaemonSets(t, c, ns)
	if len(list) == 0 {
		return false
	}
	for _, ds := range list {
		s := ds.Status
		if s.ObservedGeneration != ds.Generation || s.DesiredNumberScheduled == 0 ||
			s.UpdatedNumberScheduled != s.DesiredNumberScheduled || s.NumberReady != s.DesiredNumberScheduled ||
			s.CurrentNumberScheduled != s.DesiredNumberScheduled {
			return false
		}
	}
	return true
}

// enginesExist reports whether any engine DaemonSet or pod is left.
func enginesExist(t *testing.T, c client.Client, ns string) bool {
	t.Helper()
	return len(engineDaemonSets(t, c, ns)) > 0 || len(enginePodUIDs(t, c, ns)) > 0
}

// updateRetry gets obj by key, applies mutate and updates it, retrying on conflicts with the
// operator's status writes.
func updateRetry(t *testing.T, c client.Client, key types.NamespacedName, obj client.Object, mutate func()) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := c.Get(context.Background(), key, obj); err != nil {
			return err
		}
		mutate()
		return c.Update(context.Background(), obj)
	})
	if err != nil {
		t.Fatalf("update %s: %v", key, err)
	}
}

var (
	lostRE      = regexp.MustCompile(`Queries lost:\s+(\d+)`)
	completedRE = regexp.MustCompile(`Queries completed:\s+(\d+)`)
	rcodesRE    = regexp.MustCompile(`Response codes:\s+(.*)`)
)

// dnsperf sends 5 queries/s for seconds to serviceIP from the probe. It returns the lost count and
// whether every completed query answered NOERROR. It never calls t.Fatal, so goroutines may use it.
func dnsperf(t *testing.T, serviceIP string, seconds int) (lost int, noerror bool) {
	out, err := probeErr("", fmt.Sprintf(`q=$(mktemp); printf 'www.optest.nexora.test. A\n' > "$q"; dnsperf -s %s -d "$q" -Q 5 -l %d -t 2; rc=$?; rm -f "$q"; exit $rc`,
		serviceIP, seconds))
	if err != nil {
		t.Errorf("dnsperf %s: %v\n%s", serviceIP, err, out)
		return -1, false
	}
	m, c, r := lostRE.FindStringSubmatch(out), completedRE.FindStringSubmatch(out), rcodesRE.FindStringSubmatch(out)
	if m == nil || c == nil || r == nil {
		t.Errorf("dnsperf %s: unparsable output\n%s", serviceIP, out)
		return -1, false
	}
	lost, _ = strconv.Atoi(m[1])
	completed, _ := strconv.Atoi(c[1])
	codes := strings.TrimSpace(r[1])
	t.Logf("dnsperf %s: completed %d, lost %d, response codes %s", serviceIP, completed, lost, codes)
	return lost, completed > 0 && strings.HasPrefix(codes, "NOERROR") && !strings.Contains(codes, ",")
}

// digLoop sends 5 queries/s for seconds to serviceIP from the probe, each from a fresh dig (one try,
// 2 s timeout, like dnsperf -t 2), and returns how many did not answer 192.0.2.10. It never calls t.Fatal.
func digLoop(t *testing.T, serviceIP string, seconds int) int {
	out, err := probeErr("", fmt.Sprintf(`d=$(mktemp -d); end=$(( $(date +%%s) + %d )); n=0
while [ "$(date +%%s)" -lt "$end" ]; do
  n=$((n+1)); ( [ "$(dig +short +tries=1 +time=2 @%s www.optest.nexora.test. A)" = 192.0.2.10 ] || touch "$d/lost-$n" ) &
  sleep 0.2
done
wait; echo "sent $n lost $(ls "$d" | wc -l)"; rm -rf "$d"`, seconds, serviceIP))
	var sent, lost int
	i := strings.LastIndex(out, "sent ")
	if i < 0 {
		t.Errorf("dig loop %s: %v\n%s", serviceIP, err, out)
		return -1
	}
	if _, perr := fmt.Sscanf(out[i:], "sent %d lost %d", &sent, &lost); err != nil || perr != nil || sent == 0 {
		t.Errorf("dig loop %s: %v %v\n%s", serviceIP, err, perr, out)
		return -1
	}
	t.Logf("dig loop %s: sent %d, lost %d", serviceIP, sent, lost)
	return lost
}
