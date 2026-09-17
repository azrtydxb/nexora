package deploytest

import (
	"fmt"
	"strings"
	"testing"
)

func TestHelmQueryLogBackends(t *testing.T) {
	common := []string{"--set", "image.tag=sha-0000000", "--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}
	ch := render(t, append(common, "--set", "mgmt.querylog.backend=clickhouse",
		"--set", "mgmt.querylog.clickhouse.url=http://clickhouse.nexora.svc:8123", "--set", "mgmt.querylog.clickhouse.username=nexora_reader",
		"--set", "mgmt.querylog.clickhouse.passwordSecret.name=nexora-clickhouse", "--set", "mgmt.querylog.clickhouse.passwordSecret.key=reader-password")...)
	mgmt := find(t, ch, "Deployment", "nexora-mgmt")
	mc := container(t, mgmt, "mgmt")
	for name, want := range map[string]string{
		"NEXORA_QUERYLOG_BACKEND": "clickhouse", "NEXORA_CLICKHOUSE_URL": "http://clickhouse.nexora.svc:8123",
		"NEXORA_CLICKHOUSE_DATABASE": "nexora", "NEXORA_CLICKHOUSE_TABLE": "querylog", "NEXORA_CLICKHOUSE_USERNAME": "nexora_reader",
		"NEXORA_CLICKHOUSE_PASSWORD_FILE": "/etc/nexora/querylog/password",
	} {
		if e := env(mc, name); e == nil || e["value"] != want {
			t.Errorf("clickhouse %s = %v, want %s", name, e, want)
		}
	}
	if !strings.Contains(fmt.Sprint(mgmt.path("spec", "template", "spec", "volumes")), "nexora-clickhouse") {
		t.Error("password Secret volume missing")
	}
	if vols := fmt.Sprint(mgmt.path("spec", "template", "spec", "volumes")); !strings.Contains(vols, "key:reader-password") || !strings.Contains(vols, "path:password") {
		t.Errorf("password Secret items = %s", vols)
	}
	if mounts := fmt.Sprint(mc["volumeMounts"]); !strings.Contains(mounts, "mountPath:/etc/nexora/querylog name:querylog-password readOnly:true") {
		t.Errorf("password mount = %s", mounts)
	}

	// Positive path for the Loki password file before asserting its absence below.
	lkPass := render(t, append(common, "--set", "mgmt.querylog.backend=loki", "--set", "mgmt.querylog.loki.url=http://loki.monitoring.svc:3100",
		"--set", "mgmt.querylog.loki.username=reader", "--set", "mgmt.querylog.loki.passwordSecret.name=nexora-loki")...)
	lpm := find(t, lkPass, "Deployment", "nexora-mgmt")
	if e := env(container(t, lpm, "mgmt"), "NEXORA_LOKI_PASSWORD_FILE"); e == nil || e["value"] != "/etc/nexora/querylog/password" {
		t.Errorf("loki password file = %v", e)
	}
	if !strings.Contains(fmt.Sprint(lpm.path("spec", "template", "spec", "volumes")), "nexora-loki") {
		t.Error("loki password Secret volume missing")
	}

	lk := render(t, append(common, "--set", "mgmt.querylog.backend=loki", "--set", "mgmt.querylog.loki.url=http://loki.monitoring.svc:3100", "--set", "mgmt.querylog.loki.tenant=t1")...)
	lkm := find(t, lk, "Deployment", "nexora-mgmt")
	lc := container(t, lkm, "mgmt")
	for name, want := range map[string]string{
		"NEXORA_LOKI_URL": "http://loki.monitoring.svc:3100", "NEXORA_LOKI_SELECTOR": `{service_name="nexora-engine"}`,
		"NEXORA_LOKI_TENANT": "t1", "NEXORA_LOKI_USERNAME": "", "NEXORA_LOKI_LOOKBACK": "168h",
	} {
		if e := env(lc, name); e == nil || e["value"] != want {
			t.Errorf("loki %s = %v, want %s", name, e, want)
		}
	}
	if env(lc, "NEXORA_LOKI_PASSWORD_FILE") != nil {
		t.Error("no passwordSecret -> no password file")
	}
	if strings.Contains(fmt.Sprint(lkm.path("spec", "template", "spec", "volumes")), "querylog-password") {
		t.Error("no passwordSecret -> no password volume")
	}
	if out, err := helm(append([]string{"template", "nexora", chartDir, "--set", "mgmt.querylog.backend=loki"}, common...)...); err == nil || !strings.Contains(out, "mgmt.querylog.loki.url is required when mgmt.querylog.backend=loki") {
		t.Errorf("missing loki url: %v %s", err, out)
	}
	if out, err := helm(append([]string{"template", "nexora", chartDir, "--set", "mgmt.querylog.backend=clickhouse"}, common...)...); err == nil || !strings.Contains(out, "mgmt.querylog.clickhouse.url is required when mgmt.querylog.backend=clickhouse") {
		t.Errorf("missing clickhouse url: %v %s", err, out)
	}
	kw := render(t, "-f", "../kw/values-kw.yaml", "--set", "image.tag=sha-0000000", "--api-versions", "monitoring.coreos.com/v1")
	if e := env(container(t, find(t, kw, "Deployment", "nexora-mgmt"), "mgmt"), "NEXORA_QUERYLOG_BACKEND"); e == nil || e["value"] != "opensearch" {
		t.Errorf("kw backend = %v, want opensearch", e)
	}
}
