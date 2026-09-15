// Package config loads the management plane configuration from the environment.
package config

import (
	"fmt"
	"net/netip"
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
	// BootstrapTokenFile, when set, holds an nxt_ API token applied to the system user nexora-operator
	// at start and every BootstrapTokenReloadInterval (the Kubernetes operator's admin credential).
	BootstrapTokenFile           string
	BootstrapTokenReloadInterval time.Duration
	// KEKFile holds the base64 32-byte key-encryption key sealing secrets at rest ("" = none).
	KEKFile string
	// PKCS#11 token holding DNSSEC keys and the envelope wrap key; all three or none.
	PKCS11Module, PKCS11TokenLabel, PKCS11PinFile string
	// RolloutTick is how often the rollout controller steps open rollouts without a notification.
	RolloutTick time.Duration
	// EngineCertTTL is the lifetime of the engine certificates this plane issues.
	EngineCertTTL time.Duration
	// CatalogMirror, when set, is a base URL serving every filter catalog source at <base>/<source key>.
	CatalogMirror string
	// RepositoryURL, when set, is the https source repository the GUI links the build commit to.
	RepositoryURL string
	// TrustedProxies are the reverse proxy networks whose X-Forwarded-For names the client
	// (NEXORA_TRUSTED_PROXY_CIDRS, comma separated).
	TrustedProxies []netip.Prefix
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
		DatabaseURL:      getenv("NEXORA_DATABASE_URL"),
		HTTPListen:       get("NEXORA_HTTP_LISTEN", ":8080"),
		GRPCListen:       get("NEXORA_GRPC_LISTEN", ":9443"),
		CACertFile:       getenv("NEXORA_CA_CERT_FILE"),
		CAKeyFile:        getenv("NEXORA_CA_KEY_FILE"),
		PublicURL:        getenv("NEXORA_PUBLIC_URL"),
		QueryLogBackend:  get("NEXORA_QUERYLOG_BACKEND", "builtin"),
		OTLPEndpoint:     getenv("NEXORA_OTLP_ENDPOINT"),
		DNSTLSCertFile:   getenv("NEXORA_DNS_TLS_CERT_FILE"),
		DNSTLSKeyFile:    getenv("NEXORA_DNS_TLS_KEY_FILE"),
		KEKFile:          getenv("NEXORA_KEK_FILE"),
		PKCS11Module:     getenv("NEXORA_PKCS11_MODULE"),
		PKCS11TokenLabel: getenv("NEXORA_PKCS11_TOKEN_LABEL"),
		PKCS11PinFile:    getenv("NEXORA_PKCS11_PIN_FILE"),
		CatalogMirror:    getenv("NEXORA_CATALOG_MIRROR"),
		RepositoryURL:    getenv("NEXORA_REPOSITORY_URL"),
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
	if (c.PKCS11Module == "") != (c.PKCS11TokenLabel == "") || (c.PKCS11Module == "") != (c.PKCS11PinFile == "") {
		return Config{}, fmt.Errorf("NEXORA_PKCS11_MODULE, NEXORA_PKCS11_TOKEN_LABEL and NEXORA_PKCS11_PIN_FILE must be set together")
	}
	interval, err := time.ParseDuration(get("NEXORA_DNS_TLS_RELOAD_INTERVAL", "30s"))
	if err != nil || interval < time.Second {
		return Config{}, fmt.Errorf("NEXORA_DNS_TLS_RELOAD_INTERVAL must be a duration of at least 1s")
	}
	c.DNSTLSReloadInterval = interval
	bootstrapInterval, err := time.ParseDuration(get("NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL", "30s"))
	if err != nil || bootstrapInterval < time.Second {
		return Config{}, fmt.Errorf("NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL must be a duration of at least 1s")
	}
	c.BootstrapTokenFile, c.BootstrapTokenReloadInterval = getenv("NEXORA_BOOTSTRAP_TOKEN_FILE"), bootstrapInterval
	tick, err := time.ParseDuration(get("NEXORA_ROLLOUT_TICK", "1s"))
	if err != nil || tick < 100*time.Millisecond || tick > time.Minute {
		return Config{}, fmt.Errorf("NEXORA_ROLLOUT_TICK must be between 100ms and 1m")
	}
	c.RolloutTick = tick
	certTTL, err := time.ParseDuration(get("NEXORA_ENGINE_CERT_TTL", "2160h"))
	if err != nil || certTTL < 30*time.Second {
		return Config{}, fmt.Errorf("NEXORA_ENGINE_CERT_TTL must be a duration of at least 30s")
	}
	c.EngineCertTTL = certTTL
	if c.CatalogMirror != "" && !strings.HasPrefix(c.CatalogMirror, "http://") && !strings.HasPrefix(c.CatalogMirror, "https://") {
		return Config{}, fmt.Errorf("NEXORA_CATALOG_MIRROR must be an http(s) URL")
	}
	if c.RepositoryURL != "" && !strings.HasPrefix(c.RepositoryURL, "https://") {
		return Config{}, fmt.Errorf("NEXORA_REPOSITORY_URL must be an https URL")
	}
	for _, v := range strings.Split(getenv("NEXORA_TRUSTED_PROXY_CIDRS"), ",") {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return Config{}, fmt.Errorf("NEXORA_TRUSTED_PROXY_CIDRS: %w", err)
		}
		c.TrustedProxies = append(c.TrustedProxies, p.Masked())
	}
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
