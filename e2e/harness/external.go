package harness

import (
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
)

func externalURL(t *testing.T, name string) string {
	t.Helper()
	v := strings.TrimSuffix(os.Getenv(name), "/")
	if v == "" {
		t.Fatalf("%s is not set: point it at the shared test service (see deploy/dev/dev-pod.yaml)", name)
	}
	return v
}

// OpenSearchURL is the shared OpenSearch from NEXORA_E2E_OPENSEARCH_URL.
func OpenSearchURL(t *testing.T) string {
	t.Helper()
	return externalURL(t, "NEXORA_E2E_OPENSEARCH_URL")
}

// JaegerQueryURL is the shared Jaeger query API from NEXORA_E2E_JAEGER_QUERY_URL.
func JaegerQueryURL(t *testing.T) string {
	t.Helper()
	return externalURL(t, "NEXORA_E2E_JAEGER_QUERY_URL")
}

// JaegerOTLPEndpoint is the OTLP gRPC host:port of the Jaeger behind JaegerQueryURL.
func JaegerOTLPEndpoint(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(JaegerQueryURL(t))
	if err != nil || u.Hostname() == "" {
		t.Fatalf("NEXORA_E2E_JAEGER_QUERY_URL is not a URL: %v", err)
	}
	return net.JoinHostPort(u.Hostname(), "4317")
}
