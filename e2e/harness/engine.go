package harness

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// Engine is a running nexora-engine. DNS is the UDP+TCP host:port, Metrics the metrics
// host:port.
type Engine struct {
	DNS, Metrics, StateDir, ConfigPath string
	Proc                               *Proc

	snapPath, blobDir string
}

// StartStandaloneEngine runs nexora-engine in standalone mode on loopback with snap and blobs
// (keyed by BlobRef.Sha256) and waits until it serves snap.Version.
func (e *Env) StartStandaloneEngine(snap *controlv1.ConfigSnapshot, blobs map[string][]byte) *Engine {
	e.T.Helper()
	dir, err := os.MkdirTemp(e.Dir, "engine-")
	if err != nil {
		e.T.Fatal(err)
	}
	en := &Engine{
		StateDir: filepath.Join(dir, "state"), ConfigPath: filepath.Join(dir, "engine.toml"),
		snapPath: filepath.Join(dir, "snapshot.binpb"), blobDir: filepath.Join(dir, "blobs"),
	}
	for _, d := range []string{en.StateDir, en.blobDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			e.T.Fatal(err)
		}
	}
	en.writeSnapshot(e.T, snap, blobs)
	dnsPort, metricsPort := e.FreePort(), e.FreePort()
	en.DNS = fmt.Sprintf("127.0.0.1:%d", dnsPort)
	en.Metrics = fmt.Sprintf("127.0.0.1:%d", metricsPort)
	toml := fmt.Sprintf(`node_name = "e2e-%s"
state_dir = %q
listen_udp = ["127.0.0.1:%d"]
listen_tcp = ["127.0.0.1:%d"]
metrics_listen = "127.0.0.1:%d"
workers = 2
standalone_snapshot = %q
standalone_blob_dir = %q
`, filepath.Base(dir)[len("engine-"):], en.StateDir, dnsPort, dnsPort, metricsPort, en.snapPath, en.blobDir)
	if err := os.WriteFile(en.ConfigPath, []byte(toml), 0o600); err != nil {
		e.T.Fatal(err)
	}
	en.Proc = e.Start("nexora-engine", []string{"--config", en.ConfigPath},
		[]string{"NEXORA_DOH_RESOLVE=fixture.nexora.test=127.0.0.1"})
	en.WaitVersion(e.T, snap.Version)
	return en
}

func (en *Engine) writeSnapshot(t *testing.T, snap *controlv1.ConfigSnapshot, blobs map[string][]byte) {
	t.Helper()
	for sha, data := range blobs {
		if err := os.WriteFile(filepath.Join(en.blobDir, sha), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := proto.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	// Write-then-rename so a concurrent engine read never sees a partial file.
	tmp := en.snapPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, en.snapPath); err != nil {
		t.Fatal(err)
	}
}

// Reload writes a new snapshot and blobs, sends SIGHUP and waits until snap.Version is served.
func (en *Engine) Reload(t *testing.T, snap *controlv1.ConfigSnapshot, blobs map[string][]byte) {
	t.Helper()
	en.writeSnapshot(t, snap, blobs)
	en.Proc.Signal(syscall.SIGHUP)
	en.WaitVersion(t, snap.Version)
}

// WaitVersion waits up to 10 s for the engine to log that it serves version v.
func (en *Engine) WaitVersion(t *testing.T, v uint64) {
	t.Helper()
	re := regexp.MustCompile(fmt.Sprintf(`(?m)^nexora-engine: serving version %d\r?$`, v))
	Eventually(t, 10*time.Second, func() error {
		data, err := os.ReadFile(en.Proc.LogPath)
		if err != nil {
			return err
		}
		if re.Match(data) {
			return nil
		}
		select {
		case <-en.Proc.done:
			t.Fatalf("nexora-engine exited before serving version %d:\n%s", v, tail(en.Proc.LogPath, 50))
		default:
		}
		return fmt.Errorf("engine has not logged serving version %d:\n%s", v, tail(en.Proc.LogPath, 20))
	})
}

var metricsClient = &http.Client{Timeout: 5 * time.Second}

// Metric scrapes /metrics and returns the sum of the samples of family name whose labels include
// every pair in labels (histograms and summaries contribute their sample count).
func (en *Engine) Metric(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	resp, err := metricsClient.Get("http://" + en.Metrics + "/metrics")
	if err != nil {
		t.Fatalf("scrape metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape metrics: %s", resp.Status)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		t.Fatalf("parse metrics: %v", err)
	}
	fam, ok := families[name]
	if !ok {
		t.Fatalf("metric %s not exposed", name)
	}
	var sum float64
	for _, m := range fam.GetMetric() {
		have := map[string]string{}
		for _, lp := range m.GetLabel() {
			have[lp.GetName()] = lp.GetValue()
		}
		match := true
		for k, v := range labels {
			if have[k] != v {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		switch {
		case m.Counter != nil:
			sum += m.GetCounter().GetValue()
		case m.Gauge != nil:
			sum += m.GetGauge().GetValue()
		case m.Untyped != nil:
			sum += m.GetUntyped().GetValue()
		case m.Histogram != nil:
			sum += float64(m.GetHistogram().GetSampleCount())
		case m.Summary != nil:
			sum += float64(m.GetSummary().GetSampleCount())
		}
	}
	return sum
}
