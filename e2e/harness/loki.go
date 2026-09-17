package harness

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// LokiOptions tunes a local Loki; zero values are the kw defaults.
type LokiOptions struct {
	MaxQuerySeries int // limits_config.max_query_series; 0 = 500
}

// Loki is a local single-binary Loki. URL is its HTTP base URL.
type Loki struct {
	URL  string
	Proc *Proc
}

// lokiConfig is a single-process filesystem Loki whose limits_config copies kw's shared Loki.
const lokiConfig = `auth_enabled: false
analytics:
  reporting_enabled: false
server:
  http_listen_address: 127.0.0.1
  http_listen_port: {{HTTP}}
  grpc_listen_address: 127.0.0.1
  grpc_listen_port: {{GRPC}}
  log_level: warn
common:
  path_prefix: {{DIR}}
  storage:
    filesystem:
      chunks_directory: {{DIR}}/chunks
      rules_directory: {{DIR}}/rules
  replication_factor: 1
  instance_addr: 127.0.0.1
  ring:
    instance_addr: 127.0.0.1
    kvstore:
      store: inmemory
ingester:
  lifecycler:
    min_ready_duration: 0s
    final_sleep: 0s
schema_config:
  configs:
    - from: "2024-01-01"
      store: tsdb
      object_store: filesystem
      schema: v13
      index: { prefix: index_, period: 24h }
limits_config:
  allow_structured_metadata: true
  max_cache_freshness_per_query: 10m
  query_timeout: 300s
  reject_old_samples: true
  reject_old_samples_max_age: 168h
  retention_period: 168h
  split_queries_by_interval: 15m
  volume_enabled: true
  max_query_series: {{SERIES}}
query_range:
  align_queries_with_step: true
pattern_ingester:
  enabled: false
`

var lokiAddrInUse = regexp.MustCompile(`(?i)address already in use`)

// StartLoki starts loki on free loopback ports with its data in a temporary directory and waits
// until /ready answers ready.
func (e *Env) StartLoki(o LokiOptions) *Loki {
	e.T.Helper()
	if _, err := exec.LookPath("loki"); err != nil {
		e.T.Fatal("loki not found: rebuild the toolbox image (deploy/dev/Dockerfile)")
	}
	dir, err := os.MkdirTemp(e.Dir, "loki-")
	if err != nil {
		e.T.Fatal(err)
	}
	series := o.MaxQuerySeries
	if series == 0 {
		series = 500
	}
	cfgPath := filepath.Join(dir, "loki.yaml")
	l := &Loki{}
	for attempt := 1; ; attempt++ {
		httpPort, grpcPort := e.FreePort(), e.FreePort()
		l.URL = fmt.Sprintf("http://127.0.0.1:%d", httpPort)
		cfg := strings.NewReplacer("{{DIR}}", dir, "{{HTTP}}", fmt.Sprint(httpPort), "{{GRPC}}", fmt.Sprint(grpcPort),
			"{{SERIES}}", fmt.Sprint(series)).Replace(lokiConfig)
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
			e.T.Fatal(err)
		}
		l.Proc = e.Start("loki", []string{"-config.file=" + cfgPath}, nil)
		if l.waitReady(e, attempt < portAttempts) {
			return l
		}
	}
}

// waitReady polls GET /ready for up to 60 s. When mayRetry is set and loki exited because a port
// was taken, it returns false instead of failing the test.
func (l *Loki) waitReady(e *Env, mayRetry bool) bool {
	e.T.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case <-l.Proc.done:
			if log, _ := os.ReadFile(l.Proc.LogPath); mayRetry && lokiAddrInUse.Match(log) {
				return false
			}
			e.T.Fatalf("loki exited:\n%s", tail(l.Proc.LogPath, 50))
		default:
		}
		if resp, err := client.Get(l.URL + "/ready"); err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if strings.TrimSpace(string(body)) == "ready" {
				return true
			}
		}
		if time.Now().After(deadline) {
			e.T.Fatalf("loki not ready within 60s:\n%s", tail(l.Proc.LogPath, 50))
		}
		time.Sleep(250 * time.Millisecond)
	}
}
