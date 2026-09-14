package harness

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"text/template"
	"time"
)

// OtelcolConfig selects the collector's optional exporters; an empty field omits its exporter.
type OtelcolConfig struct {
	OpenSearchURL, JaegerOTLP string
	DebugFile                 string
}

// Otelcol is a running otelcol-contrib. OTLPGRPC is its OTLP gRPC receiver host:port.
type Otelcol struct {
	OTLPGRPC   string
	Proc       *Proc
	ConfigPath string
}

var otelcolTemplate = template.Must(template.New("otelcol").Parse(`receivers:
  otlp:
    protocols:
      grpc: { endpoint: "{{.GRPC}}" }
processors:
  batch: { timeout: 200ms }
{{- if .OpenSearchURL}}
  transform/querylog:
    log_statements:
      - context: log
        statements:
          - set(attributes["nexora.filter.result"], attributes["nexora.filter"]) where attributes["nexora.filter"] != nil
          - delete_key(attributes, "nexora.filter")
{{- end}}
exporters:
  debug: { verbosity: basic }
{{- if .DebugFile}}
  file: { path: "{{.DebugFile}}" }
{{- end}}
{{- if .OpenSearchURL}}
  opensearch:
    http:
      { endpoint: "{{.OpenSearchURL}}", tls: { insecure_skip_verify: true } }
    logs_index: "nexora-querylog-v2"
    logs_index_time_format: "yyyy.MM.dd"
{{- end}}
{{- if .JaegerOTLP}}
  otlp/jaeger:
    endpoint: "{{.JaegerOTLP}}"
    tls: { insecure: true }
{{- end}}
service:
  telemetry: { metrics: { level: none } }
  pipelines:
    logs:
      {
        receivers: [otlp],
        processors: [batch],
        exporters: [debug{{if .DebugFile}}, file{{end}}],
      }
{{- if .OpenSearchURL}}
    logs/opensearch:
      {
        receivers: [otlp],
        processors: [batch, transform/querylog],
        exporters: [opensearch],
      }
{{- end}}
    traces:
      {
        receivers: [otlp],
        processors: [batch],
        exporters: [debug{{if .JaegerOTLP}}, otlp/jaeger{{end}}],
      }
    metrics: { receivers: [otlp], processors: [batch], exporters: [debug] }
`))

// StartOtelcol writes a collector config for cfg and starts otelcol-contrib, waiting until it
// reports ready and its OTLP gRPC port accepts connections. The collector cannot report a port-0
// listener, so a free port is picked and the start retried with another one when it was taken.
func (e *Env) StartOtelcol(cfg OtelcolConfig) *Otelcol {
	e.T.Helper()
	dir, err := os.MkdirTemp(e.Dir, "otelcol-")
	if err != nil {
		e.T.Fatal(err)
	}
	o := &Otelcol{ConfigPath: filepath.Join(dir, "config.yaml")}
	for attempt := 1; ; attempt++ {
		o.OTLPGRPC = fmt.Sprintf("127.0.0.1:%d", e.FreePort())
		var buf bytes.Buffer
		if err := otelcolTemplate.Execute(&buf, struct {
			OtelcolConfig
			GRPC string
		}{cfg, o.OTLPGRPC}); err != nil {
			e.T.Fatal(err)
		}
		if err := os.WriteFile(o.ConfigPath, buf.Bytes(), 0o600); err != nil {
			e.T.Fatal(err)
		}
		if o.start(e, attempt < portAttempts) {
			return o
		}
	}
}

var (
	otelcolReady   = regexp.MustCompile(`Everything is ready`)
	addressInUseRE = regexp.MustCompile(`address already in use`)
)

// start runs the collector and waits until it is ready. When mayRetry is set and the collector
// exited because its port was taken, start returns false instead of failing the test.
func (o *Otelcol) start(e *Env, mayRetry bool) bool {
	e.T.Helper()
	o.Proc = e.Start("otelcol-contrib", []string{"--config", o.ConfigPath}, nil)
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case <-o.Proc.done:
			log, _ := os.ReadFile(o.Proc.LogPath)
			if mayRetry && addressInUseRE.Match(log) {
				return false
			}
			e.T.Fatalf("otelcol %s: otelcol-contrib exited:\n%s", o.OTLPGRPC, tail(o.Proc.LogPath, 50))
		default:
		}
		// Readiness comes from the log, not a dial alone: a dial could reach whatever process
		// took the port while the collector is still failing to bind it.
		log, _ := os.ReadFile(o.Proc.LogPath)
		if otelcolReady.Match(log) {
			if c, err := net.DialTimeout("tcp", o.OTLPGRPC, 200*time.Millisecond); err == nil {
				_ = c.Close()
				return true
			}
		}
		if time.Now().After(deadline) {
			e.T.Fatalf("otelcol-contrib not ready within 20s:\n%s", tail(o.Proc.LogPath, 50))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Stop stops the collector (simulating an exporter outage).
func (o *Otelcol) Stop() {
	o.Proc.Stop()
}

// Restart stops the collector if running and starts it again on the same port and config: the
// exporters point at that port. A port another process holds is retried for 10 s.
func (o *Otelcol) Restart(e *Env) {
	e.T.Helper()
	o.Proc.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for !o.start(e, time.Now().Before(deadline)) {
		time.Sleep(200 * time.Millisecond)
	}
}
