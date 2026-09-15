package deploytest

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type composeService struct {
	Image       string            `yaml:"image"`
	Command     []string          `yaml:"command"`
	User        string            `yaml:"user"`
	Profiles    []string          `yaml:"profiles"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Sysctls     map[string]string `yaml:"sysctls"`
	DependsOn   map[string]struct {
		Condition string `yaml:"condition"`
	} `yaml:"depends_on"`
	Healthcheck *struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
}

// TestComposeExample checks deploy/compose statically; scripts/compose-verify.sh starts it on a Docker host.
func TestComposeExample(t *testing.T) {
	raw, err := os.ReadFile("../compose/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]composeService `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"postgres", "volume-init", "ca-init", "migrate", "mgmt", "engine", "otel-collector"} {
		if _, ok := c.Services[s]; !ok {
			t.Fatalf("service %s missing", s)
		}
	}
	pg, ca, mgmt, engine, otel := c.Services["postgres"], c.Services["ca-init"], c.Services["mgmt"], c.Services["engine"], c.Services["otel-collector"]
	if pg.Healthcheck == nil || pg.Environment["POSTGRES_PASSWORD_FILE"] != "/run/secrets/postgres-password" {
		t.Error("postgres needs a healthcheck and its password from the secret file")
	}
	if !strings.Contains(c.Services["migrate"].Environment["NEXORA_DATABASE_URL"], "passfile=/run/secrets/pgpass") {
		t.Error("migrate must read the database password from the pgpass secret")
	}
	if strings.Join(ca.Command, " ") != "ca init --out /var/lib/nexora-ca --if-missing" || ca.DependsOn["volume-init"].Condition != "service_completed_successfully" {
		t.Errorf("ca-init = %+v", ca)
	}
	if mgmt.DependsOn["migrate"].Condition != "service_completed_successfully" || mgmt.DependsOn["ca-init"].Condition != "service_completed_successfully" {
		t.Errorf("mgmt depends_on = %+v", mgmt.DependsOn)
	}
	if !strings.Contains(mgmt.Image, "/nexora-mgmt:") || !strings.Contains(engine.Image, "/nexora-engine:") {
		t.Errorf("images mgmt=%q engine=%q", mgmt.Image, engine.Image)
	}
	for _, k := range []string{"NEXORA_DATABASE_URL", "NEXORA_CA_CERT_FILE", "NEXORA_CA_KEY_FILE", "NEXORA_GRPC_SERVER_NAMES", "NEXORA_PUBLIC_URL"} {
		if mgmt.Environment[k] == "" {
			t.Errorf("mgmt environment lacks %s", k)
		}
	}
	if !strings.Contains(mgmt.Environment["NEXORA_GRPC_SERVER_NAMES"], "mgmt") {
		t.Error("the gRPC server certificate must name the compose service mgmt")
	}
	if len(engine.Profiles) != 1 || engine.Profiles[0] != "engine" || len(otel.Profiles) != 1 || otel.Profiles[0] != "otel" {
		t.Errorf("profiles engine=%v otel=%v", engine.Profiles, otel.Profiles)
	}
	if engine.Sysctls["net.ipv4.ip_unprivileged_port_start"] != "0" {
		t.Error("the engine runs as uid 10001 and needs net.ipv4.ip_unprivileged_port_start=0 for port 53")
	}
	udp := false
	for _, p := range engine.Ports {
		udp = udp || strings.HasSuffix(p, ":53/udp")
	}
	if !udp {
		t.Errorf("engine ports %v lack DNS over UDP", engine.Ports)
	}
	if !strings.Contains(mgmt.Environment["NEXORA_DATABASE_URL"], "passfile=/run/secrets/pgpass") {
		t.Error("mgmt must read the database password from the pgpass secret")
	}
	env, err := os.ReadFile("../compose/.env.example")
	if err != nil || !strings.Contains(string(env), "NEXORA_TAG=") || strings.Contains(strings.ToUpper(string(env)), "PASSWORD") {
		t.Errorf(".env.example must set NEXORA_TAG and hold no password: %v %q", err, env)
	}
	toml, err := os.ReadFile("../compose/engine.toml")
	if err != nil || !strings.Contains(string(toml), `management_urls = ["https://mgmt:9443"]`) {
		t.Errorf("engine.toml: %v %q", err, toml)
	}
	ops, err := os.ReadFile("../../docs/operations.md")
	if err != nil || strings.Contains(string(ops), "it is not started") || !strings.Contains(string(ops), "scripts/compose-verify.sh") {
		t.Errorf("docs/operations.md must describe the real Compose run (scripts/compose-verify.sh): %v", err)
	}
	if _, err := os.Stat("../../scripts/compose-verify.sh"); err != nil {
		t.Errorf("scripts/compose-verify.sh: %v", err)
	}
}
