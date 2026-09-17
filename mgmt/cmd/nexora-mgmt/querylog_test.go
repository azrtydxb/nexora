package main

import (
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/config"
)

func TestServeBuildsQueryLogBackend(t *testing.T) {
	for _, tc := range []struct {
		cfg     config.Config
		name    string
		builtin bool
	}{
		{config.Config{QueryLogBackend: "builtin", QueryLogBuiltinCapacity: 10}, "builtin", true},
		{config.Config{QueryLogBackend: "opensearch", OpenSearch: config.OpenSearchConfig{URL: "http://os:9200"}}, "opensearch", false},
		{config.Config{QueryLogBackend: "clickhouse", ClickHouse: config.ClickHouseConfig{URL: "http://ch:8123", Database: "nexora", Table: "querylog", Username: "r"}}, "clickhouse", false},
		{config.Config{QueryLogBackend: "loki", Loki: config.LokiConfig{URL: "http://loki:3100", Selector: `{service_name="nexora-engine"}`, Lookback: 168 * 3600e9}}, "loki", false},
	} {
		b, bi, err := buildQueryLog(tc.cfg)
		if err != nil || b.Name() != tc.name || (bi != nil) != tc.builtin {
			t.Errorf("%s: %v %v %v", tc.name, b, bi, err)
		}
		if got := queryLogToManagement(tc.cfg); got != tc.builtin {
			t.Errorf("%s: QueryLogToManagement = %v", tc.name, got)
		}
	}
	if _, _, err := buildQueryLog(config.Config{QueryLogBackend: "clickhouse", ClickHouse: config.ClickHouseConfig{URL: "http://ch:8123", Database: "nexora", Table: "querylog", Username: "r", PasswordFile: "/nonexistent"}}); err == nil || !strings.Contains(err.Error(), "/nonexistent") {
		t.Fatal("unreadable password file must fail")
	}
}
