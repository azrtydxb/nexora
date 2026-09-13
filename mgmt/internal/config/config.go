// Package config loads the management plane configuration from the environment.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is the management plane configuration (see docs/architecture.md).
type Config struct {
	DatabaseURL, HTTPListen, GRPCListen, CACertFile, CAKeyFile string
	GRPCServerNames                                            []string
	PublicURL                                                  string
	SecureCookies                                              bool
	OIDC                                                       OIDCConfig
	QueryLogBackend                                            string
	QueryLogBuiltinCapacity                                    int
	OpenSearch                                                 OpenSearchConfig
	OTLPEndpoint                                               string
	// DNS serving certificate (DoT, DoH, DoQ) pushed to engines; both files or neither.
	DNSTLSCertFile, DNSTLSKeyFile string
	DNSTLSReloadInterval          time.Duration
}

// OIDCConfig configures the optional OIDC login.
type OIDCConfig struct {
	Issuer, ClientID, ClientSecretFile, AdminGroup, OperatorGroup string
}

// Enabled reports whether an OIDC issuer is configured.
func (o OIDCConfig) Enabled() bool { return o.Issuer != "" }

// OpenSearchConfig configures the OpenSearch query-log backend.
type OpenSearchConfig struct {
	URL, Index, Username, PasswordFile string
}

// Load reads the configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	c := Config{
		DatabaseURL:     getenv("NEXORA_DATABASE_URL"),
		HTTPListen:      get("NEXORA_HTTP_LISTEN", ":8080"),
		GRPCListen:      get("NEXORA_GRPC_LISTEN", ":9443"),
		CACertFile:      getenv("NEXORA_CA_CERT_FILE"),
		CAKeyFile:       getenv("NEXORA_CA_KEY_FILE"),
		PublicURL:       getenv("NEXORA_PUBLIC_URL"),
		QueryLogBackend: get("NEXORA_QUERYLOG_BACKEND", "builtin"),
		OTLPEndpoint:    getenv("NEXORA_OTLP_ENDPOINT"),
		DNSTLSCertFile:  getenv("NEXORA_DNS_TLS_CERT_FILE"),
		DNSTLSKeyFile:   getenv("NEXORA_DNS_TLS_KEY_FILE"),
		OIDC: OIDCConfig{
			Issuer:           getenv("NEXORA_OIDC_ISSUER"),
			ClientID:         getenv("NEXORA_OIDC_CLIENT_ID"),
			ClientSecretFile: getenv("NEXORA_OIDC_CLIENT_SECRET_FILE"),
			AdminGroup:       getenv("NEXORA_OIDC_ADMIN_GROUP"),
			OperatorGroup:    getenv("NEXORA_OIDC_OPERATOR_GROUP"),
		},
		OpenSearch: OpenSearchConfig{
			URL:          getenv("NEXORA_OPENSEARCH_URL"),
			Index:        get("NEXORA_OPENSEARCH_INDEX", "nexora-querylog-*"),
			Username:     getenv("NEXORA_OPENSEARCH_USERNAME"),
			PasswordFile: getenv("NEXORA_OPENSEARCH_PASSWORD_FILE"),
		},
	}
	for _, req := range []struct{ key, val string }{
		{"NEXORA_DATABASE_URL", c.DatabaseURL},
		{"NEXORA_CA_CERT_FILE", c.CACertFile},
		{"NEXORA_CA_KEY_FILE", c.CAKeyFile},
	} {
		if req.val == "" {
			return Config{}, fmt.Errorf("%s is required", req.key)
		}
	}
	cookies, err := strconv.ParseBool(get("NEXORA_SECURE_COOKIES", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("NEXORA_SECURE_COOKIES: %w", err)
	}
	c.SecureCookies = cookies
	capacity, err := strconv.Atoi(get("NEXORA_QUERYLOG_BUILTIN_CAPACITY", "200000"))
	if err != nil || capacity <= 0 {
		return Config{}, fmt.Errorf("NEXORA_QUERYLOG_BUILTIN_CAPACITY must be a positive integer")
	}
	c.QueryLogBuiltinCapacity = capacity
	for _, n := range strings.Split(getenv("NEXORA_GRPC_SERVER_NAMES"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			c.GRPCServerNames = append(c.GRPCServerNames, n)
		}
	}
	switch c.QueryLogBackend {
	case "builtin":
	case "opensearch":
		if c.OpenSearch.URL == "" {
			return Config{}, fmt.Errorf("NEXORA_OPENSEARCH_URL is required when NEXORA_QUERYLOG_BACKEND=opensearch")
		}
	default:
		return Config{}, fmt.Errorf("NEXORA_QUERYLOG_BACKEND must be builtin or opensearch, got %q", c.QueryLogBackend)
	}
	if (c.DNSTLSCertFile == "") != (c.DNSTLSKeyFile == "") {
		return Config{}, fmt.Errorf("NEXORA_DNS_TLS_CERT_FILE and NEXORA_DNS_TLS_KEY_FILE must be set together")
	}
	interval, err := time.ParseDuration(get("NEXORA_DNS_TLS_RELOAD_INTERVAL", "30s"))
	if err != nil || interval < time.Second {
		return Config{}, fmt.Errorf("NEXORA_DNS_TLS_RELOAD_INTERVAL must be a duration of at least 1s")
	}
	c.DNSTLSReloadInterval = interval
	if c.OIDC.Enabled() {
		if c.OIDC.ClientID == "" {
			return Config{}, fmt.Errorf("NEXORA_OIDC_CLIENT_ID is required when NEXORA_OIDC_ISSUER is set")
		}
		if c.OIDC.ClientSecretFile == "" {
			return Config{}, fmt.Errorf("NEXORA_OIDC_CLIENT_SECRET_FILE is required when NEXORA_OIDC_ISSUER is set")
		}
	}
	return c, nil
}
