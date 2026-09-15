package deploytest

import (
	"os"
	"strings"
	"testing"
)

var base = []string{"--set", "mgmt.ca.existingSecret=ca", "--api-versions", "postgresql.cnpg.io/v1",
	"--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}

func with(extra ...string) []string { return append(append([]string{}, base...), extra...) }

func renderErr(t *testing.T, args ...string) string {
	t.Helper()
	out, err := helm(append([]string{"template", "nexora", chartDir, "--namespace", "nexora"}, args...)...)
	if err == nil {
		t.Fatalf("helm template %v succeeded, want a failure", args)
	}
	return out
}

func TestHelmKwRenderUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/kw-render.golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := helm("template", "nexora", chartDir, "--namespace", "nexora", "-f", "../kw/values-kw.yaml",
		"--api-versions", "monitoring.coreos.com/v1", "--set", "image.tag=golden")
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, got)
	}
	if got != string(want) {
		t.Fatal("the kw render changed; production output must stay byte-identical (compare with testdata/kw-render.golden.yaml)")
	}
}

func TestHelmCNPGHighAvailability(t *testing.T) {
	docs := render(t, with("--set", "database.cnpg.instances=3", "--set", "database.cnpg.antiAffinity=required",
		"--set", "database.cnpg.primaryUpdateMethod=switchover", "--set", "database.cnpg.resources.requests.memory=512Mi",
		"--set", "database.cnpg.postgresql.parameters.max_connections=200")...)
	c := find(t, docs, "Cluster", "nexora-db")
	checks := map[string][2]any{
		"instances":      {c.path("spec", "instances"), 3},
		"antiAffinity":   {c.path("spec", "affinity", "enablePodAntiAffinity"), true},
		"topologyKey":    {c.path("spec", "affinity", "topologyKey"), "kubernetes.io/hostname"},
		"type":           {c.path("spec", "affinity", "podAntiAffinityType"), "required"},
		"updateStrategy": {c.path("spec", "primaryUpdateStrategy"), "unsupervised"},
		"updateMethod":   {c.path("spec", "primaryUpdateMethod"), "switchover"},
		// CNPG's own default (180 s) makes a primary deletion fail over in about three minutes, past the
		// 120 s target, because it waits for the management plane's pooled sessions.
		"smartShutdown":   {c.path("spec", "smartShutdownTimeout"), 30},
		"memory":          {c.path("spec", "resources", "requests", "memory"), "512Mi"},
		"max_connections": {c.path("spec", "postgresql", "parameters", "max_connections"), "200"},
	}
	for name, v := range checks {
		if v[0] != v[1] {
			t.Errorf("%s = %v, want %v", name, v[0], v[1])
		}
	}
	ref := env(container(t, find(t, docs, "Deployment", "nexora-mgmt"), "mgmt"), "NEXORA_DATABASE_URL").path("valueFrom", "secretKeyRef").(map[string]any)
	if ref["name"] != "nexora-db-app" || ref["key"] != "uri" {
		t.Errorf("mgmt database secret = %v", ref)
	}
	if has(docs, "ScheduledBackup") || c.path("spec", "backup") != nil {
		t.Error("backup renders while disabled")
	}
	raised := find(t, render(t, with("--set", "database.cnpg.smartShutdownTimeout=120")...), "Cluster", "nexora-db")
	if raised.path("spec", "smartShutdownTimeout") != 120 {
		t.Errorf("smartShutdownTimeout not configurable: %v", raised.path("spec", "smartShutdownTimeout"))
	}
}

func TestHelmCNPGBackups(t *testing.T) {
	b := []string{"--set", "database.cnpg.backup.enabled=true", "--set", "database.cnpg.backup.destinationPath=s3://bkt/nexora",
		"--set", "database.cnpg.backup.endpointURL=http://minio:9000", "--set", "database.cnpg.backup.s3Credentials.existingSecret=s3",
		"--set", "database.cnpg.backup.schedule=0 30 2 * * *", "--set", "database.cnpg.backup.immediate=true"}
	docs := render(t, with(b...)...)
	c := find(t, docs, "Cluster", "nexora-db")
	bos := obj(c.path("spec", "backup", "barmanObjectStore").(map[string]any))
	for path, want := range map[string]any{
		"destinationPath": "s3://bkt/nexora", "endpointURL": "http://minio:9000", "serverName": "nexora-db",
	} {
		if bos[path] != want {
			t.Errorf("barmanObjectStore.%s = %v, want %v", path, bos[path], want)
		}
	}
	if bos.path("s3Credentials", "accessKeyId", "name") != "s3" || bos.path("s3Credentials", "accessKeyId", "key") != "ACCESS_KEY_ID" ||
		bos.path("s3Credentials", "secretAccessKey", "key") != "ACCESS_SECRET_KEY" {
		t.Errorf("s3Credentials = %v", bos["s3Credentials"])
	}
	if bos.path("wal", "compression") != "gzip" || bos.path("data", "compression") != "gzip" || c.path("spec", "backup", "retentionPolicy") != "30d" {
		t.Errorf("compression/retention = %v", c.path("spec", "backup"))
	}
	if bos["endpointCA"] != nil {
		t.Error("endpointCA renders without a secret")
	}
	sb := find(t, docs, "ScheduledBackup", "nexora-db-scheduled")
	if sb.path("spec", "schedule") != "0 30 2 * * *" || sb.path("spec", "cluster", "name") != "nexora-db" ||
		sb.path("spec", "method") != "barmanObjectStore" || sb.path("spec", "backupOwnerReference") != "self" || sb.path("spec", "immediate") != true {
		t.Errorf("scheduled backup = %v", sb["spec"])
	}
	if out := renderErr(t, with("--set", "database.cnpg.backup.enabled=true", "--set", "database.cnpg.backup.s3Credentials.existingSecret=s3")...); !strings.Contains(out, "database.cnpg.backup.destinationPath is required") {
		t.Errorf("missing path error: %s", out)
	}
	if out := renderErr(t, with("--set", "database.cnpg.backup.enabled=true", "--set", "database.cnpg.backup.destinationPath=s3://b/p")...); !strings.Contains(out, "database.cnpg.backup.s3Credentials.existingSecret is required") {
		t.Errorf("missing credentials error: %s", out)
	}
}

func TestHelmCNPGRecovery(t *testing.T) {
	b := []string{"--set", "database.cnpg.backup.destinationPath=s3://bkt/nexora", "--set", "database.cnpg.backup.endpointURL=http://minio:9000",
		"--set", "database.cnpg.backup.s3Credentials.existingSecret=s3", "--set", "database.cnpg.recovery.enabled=true",
		"--set", "database.cnpg.recovery.sourceServerName=nexora-db", "--set", "database.cnpg.clusterName=nexora-db-restore"}
	docs := render(t, with(b...)...)
	c := find(t, docs, "Cluster", "nexora-db-restore")
	if c.path("spec", "bootstrap", "initdb") != nil {
		t.Error("initdb renders next to recovery")
	}
	if c.path("spec", "bootstrap", "recovery", "source") != "backup-source" || c.path("spec", "bootstrap", "recovery", "database") != "nexora" || c.path("spec", "bootstrap", "recovery", "owner") != "nexora" {
		t.Errorf("recovery = %v", c.path("spec", "bootstrap"))
	}
	ext := c.path("spec", "externalClusters").([]any)
	e0 := obj(ext[0].(map[string]any))
	if e0["name"] != "backup-source" || e0.path("barmanObjectStore", "serverName") != "nexora-db" ||
		e0.path("barmanObjectStore", "destinationPath") != "s3://bkt/nexora" || e0.path("barmanObjectStore", "endpointURL") != "http://minio:9000" ||
		e0.path("barmanObjectStore", "s3Credentials", "accessKeyId", "name") != "s3" {
		t.Errorf("external cluster = %v", e0)
	}
	withTarget := render(t, with(append(b, "--set", "database.cnpg.recovery.targetTime=2026-09-15T03:00:00Z")...)...)
	if find(t, withTarget, "Cluster", "nexora-db-restore").path("spec", "bootstrap", "recovery", "recoveryTarget", "targetTime") != "2026-09-15T03:00:00Z" {
		t.Error("targetTime not rendered")
	}
	collide := with("--set", "database.cnpg.backup.enabled=true", "--set", "database.cnpg.backup.destinationPath=s3://bkt/nexora",
		"--set", "database.cnpg.backup.s3Credentials.existingSecret=s3", "--set", "database.cnpg.recovery.enabled=true",
		"--set", "database.cnpg.recovery.sourceServerName=nexora-db")
	if out := renderErr(t, collide...); !strings.Contains(out, "would archive into the backup it restores from") {
		t.Errorf("collision error: %s", out)
	}
	if out := renderErr(t, with("--set", "database.cnpg.recovery.enabled=true")...); !strings.Contains(out, "database.cnpg.recovery.sourceServerName is required") {
		t.Errorf("missing source error: %s", out)
	}
}

func TestHelmBootstrapToken(t *testing.T) {
	off := container(t, find(t, render(t, base...), "Deployment", "nexora-mgmt"), "mgmt")
	if env(off, "NEXORA_BOOTSTRAP_TOKEN_FILE") != nil {
		t.Error("bootstrap token env renders without the value")
	}
	docs := render(t, with("--set", "mgmt.bootstrapToken.existingSecret=tok")...)
	d := find(t, docs, "Deployment", "nexora-mgmt")
	c := container(t, d, "mgmt")
	if e := env(c, "NEXORA_BOOTSTRAP_TOKEN_FILE"); e == nil || e["value"] != "/etc/nexora/bootstrap-token/token" {
		t.Errorf("env = %v", e)
	}
	var mounted, volume bool
	for _, m := range c["volumeMounts"].([]any) {
		mm := m.(map[string]any)
		mounted = mounted || (mm["name"] == "bootstrap-token" && mm["mountPath"] == "/etc/nexora/bootstrap-token" && mm["readOnly"] == true)
	}
	for _, v := range d.path("spec", "template", "spec", "volumes").([]any) {
		vv := obj(v.(map[string]any))
		volume = volume || (vv["name"] == "bootstrap-token" && vv.path("secret", "secretName") == "tok" && vv.path("secret", "defaultMode") == 288)
	}
	if !mounted || !volume {
		t.Errorf("mount %v volume %v", mounted, volume)
	}
}
