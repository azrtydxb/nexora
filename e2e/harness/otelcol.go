package harness

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
exporters:
  debug: { verbosity: basic }
{{- if .DebugFile}}
  file: { path: "{{.DebugFile}}" }
{{- end}}
{{- if .OpenSearchURL}}
  opensearch:
    http:
      { endpoint: "{{.OpenSearchURL}}", tls: { insecure_skip_verify: true } }
    logs_index: "nexora-querylog"
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
        exporters: [debug{{if .DebugFile}}, file{{end}}{{if .OpenSearchURL}}, opensearch{{end}}],
      }
    traces:
      {
        receivers: [otlp],
        processors: [batch],
        exporters: [debug{{if .JaegerOTLP}}, otlp/jaeger{{end}}],
      }
    metrics: { receivers: [otlp], processors: [batch], exporters: [debug] }
`))

// StartOtelcol writes a collector config for cfg and starts otelcol-contrib, waiting until its
// OTLP gRPC port accepts connections.
func (e *Env) StartOtelcol(cfg OtelcolConfig) *Otelcol {
	e.T.Helper()
	dir, err := os.MkdirTemp(e.Dir, "otelcol-")
	if err != nil {
		e.T.Fatal(err)
	}
	o := &Otelcol{OTLPGRPC: fmt.Sprintf("127.0.0.1:%d", e.FreePort()), ConfigPath: filepath.Join(dir, "config.yaml")}
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
	o.start(e)
	return o
}

func (o *Otelcol) start(e *Env) {
	e.T.Helper()
	o.Proc = e.Start("otelcol-contrib", []string{"--config", o.ConfigPath}, nil)
	Eventually(e.T, 20*time.Second, func() error {
		select {
		case <-o.Proc.done:
			e.T.Fatalf("otelcol-contrib exited:\n%s", tail(o.Proc.LogPath, 50))
		default:
		}
		c, err := net.DialTimeout("tcp", o.OTLPGRPC, 200*time.Millisecond)
		if err != nil {
			return err
		}
		return c.Close()
	})
}

// Stop stops the collector (simulating an exporter outage).
func (o *Otelcol) Stop() {
	o.Proc.Stop()
}

// Restart stops the collector if running and starts it again on the same port and config.
func (o *Otelcol) Restart(e *Env) {
	e.T.Helper()
	o.Proc.Stop()
	o.start(e)
}
