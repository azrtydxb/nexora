package config_test

import (
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/config"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := config.Load(env(map[string]string{"NEXORA_DATABASE_URL": "postgres://x/y", "NEXORA_CA_CERT_FILE": "/ca.crt", "NEXORA_CA_KEY_FILE": "/ca.key"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPListen != ":8080" || c.GRPCListen != ":9443" || !c.SecureCookies || c.QueryLogBackend != "builtin" || c.QueryLogBuiltinCapacity != 200000 || c.OpenSearch.Index != "nexora-querylog-*" || c.DNSTLSReloadInterval != 30*time.Second || c.RolloutTick != time.Second {
		t.Fatalf("defaults wrong: %+v", c)
	}
	if c.OIDC.Enabled() {
		t.Fatal("OIDC must be disabled without an issuer")
	}
}

func TestLoadValidation(t *testing.T) {
	base := map[string]string{"NEXORA_DATABASE_URL": "postgres://x/y", "NEXORA_CA_CERT_FILE": "/c", "NEXORA_CA_KEY_FILE": "/k"}
	for name, mutate := range map[string]func(m map[string]string){
		"missing db":         func(m map[string]string) { delete(m, "NEXORA_DATABASE_URL") },
		"bad backend":        func(m map[string]string) { m["NEXORA_QUERYLOG_BACKEND"] = "loki" },
		"opensearch no url":  func(m map[string]string) { m["NEXORA_QUERYLOG_BACKEND"] = "opensearch" },
		"bad cookies":        func(m map[string]string) { m["NEXORA_SECURE_COOKIES"] = "maybe" },
		"oidc no client":     func(m map[string]string) { m["NEXORA_OIDC_ISSUER"] = "https://idp" },
		"dns tls cert only":  func(m map[string]string) { m["NEXORA_DNS_TLS_CERT_FILE"] = "/tls.crt" },
		"dns tls key only":   func(m map[string]string) { m["NEXORA_DNS_TLS_KEY_FILE"] = "/tls.key" },
		"dns tls interval":   func(m map[string]string) { m["NEXORA_DNS_TLS_RELOAD_INTERVAL"] = "500ms" },
		"rollout tick":       func(m map[string]string) { m["NEXORA_ROLLOUT_TICK"] = "50ms" },
		"pkcs11 module only": func(m map[string]string) { m["NEXORA_PKCS11_MODULE"] = "/usr/lib/softhsm/libsofthsm2.so" },
		"pkcs11 no pin file": func(m map[string]string) {
			m["NEXORA_PKCS11_MODULE"], m["NEXORA_PKCS11_TOKEN_LABEL"] = "/usr/lib/softhsm/libsofthsm2.so", "nexora"
		},
	} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		mutate(m)
		if _, err := config.Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	c, err := config.Load(env(map[string]string{"NEXORA_DATABASE_URL": "postgres://x/y", "NEXORA_CA_CERT_FILE": "/c", "NEXORA_CA_KEY_FILE": "/k", "NEXORA_GRPC_SERVER_NAMES": "mgmt, 10.0.0.5"}))
	if err != nil || len(c.GRPCServerNames) != 2 || c.GRPCServerNames[1] != "10.0.0.5" {
		t.Fatalf("server names: %v %v", c.GRPCServerNames, err)
	}
	c, err = config.Load(env(map[string]string{"NEXORA_DATABASE_URL": "postgres://x/y", "NEXORA_CA_CERT_FILE": "/c", "NEXORA_CA_KEY_FILE": "/k",
		"NEXORA_PKCS11_MODULE": "/m.so", "NEXORA_PKCS11_TOKEN_LABEL": "nexora", "NEXORA_PKCS11_PIN_FILE": "/pin"}))
	if err != nil || c.PKCS11Module != "/m.so" || c.PKCS11TokenLabel != "nexora" || c.PKCS11PinFile != "/pin" {
		t.Fatalf("pkcs11 settings: %+v %v", c, err)
	}
}
