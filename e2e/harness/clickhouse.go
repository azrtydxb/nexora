package harness

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ClickHouse is a local clickhouse server holding the Nexora query-log table
// (deploy/clickhouse/querylog.sql). The writer may only insert into the table and the reader may
// only select from it, like the kw users.
type ClickHouse struct {
	HTTPURL, NativeAddr, Database, Table   string
	WriterUser, WriterPassword, ReaderUser string
	ReaderPasswordFile                     string
	Proc                                   *Proc
}

const clickHouseConfig = `<clickhouse>
  <logger>
    <level>warning</level>
    <log>{{DIR}}/clickhouse.log</log>
    <errorlog>{{DIR}}/clickhouse.err.log</errorlog>
  </logger>
  <listen_host>127.0.0.1</listen_host>
  <http_port>{{HTTP}}</http_port>
  <tcp_port>{{TCP}}</tcp_port>
  <path>{{DIR}}/data/</path>
  <tmp_path>{{DIR}}/tmp/</tmp_path>
  <user_files_path>{{DIR}}/user_files/</user_files_path>
  <format_schema_path>{{DIR}}/format_schemas/</format_schema_path>
  <mark_cache_size>268435456</mark_cache_size>
  <mlock_executable>false</mlock_executable>
  <user_directories>
    <users_xml><path>{{DIR}}/users.xml</path></users_xml>
  </user_directories>
</clickhouse>
`

const clickHouseUsers = `<clickhouse>
  <profiles><default/></profiles>
  <quotas><default/></quotas>
</clickhouse>
`

// clickHouseNexoraUsers has the structure of the kw ConfigMap's users.d/nexora.xml; kw reads the
// passwords from the environment.
const clickHouseNexoraUsers = `<clickhouse>
  <users>
    <default>
      <password></password>
      <networks><ip>127.0.0.1</ip></networks>
      <profile>default</profile>
      <quota>default</quota>
    </default>
    <nexora_writer>
      <password>{{WRITER}}</password>
      <networks><ip>127.0.0.1</ip></networks>
      <profile>default</profile>
      <quota>default</quota>
      <grants><query>GRANT INSERT ON nexora.querylog</query></grants>
    </nexora_writer>
    <nexora_reader>
      <password>{{READER}}</password>
      <networks><ip>127.0.0.1</ip></networks>
      <profile>default</profile>
      <quota>default</quota>
      <grants><query>GRANT SELECT ON nexora.querylog</query></grants>
    </nexora_reader>
  </users>
</clickhouse>
`

var clickHouseAddrInUse = regexp.MustCompile(`(?i)address already in use`)

// StartClickHouse starts clickhouse server on free loopback ports in a temporary directory,
// applies deploy/clickhouse/querylog.sql twice (proving it idempotent) and returns the server.
// The users' grants live in users.d, as on kw.
func (e *Env) StartClickHouse() *ClickHouse {
	e.T.Helper()
	if _, err := exec.LookPath("clickhouse"); err != nil {
		e.T.Fatal("clickhouse not found: rebuild the toolbox image (deploy/dev/Dockerfile)")
	}
	dir, err := os.MkdirTemp(e.Dir, "clickhouse-")
	if err != nil {
		e.T.Fatal(err)
	}
	reader := randomHex(e, 16)
	ch := &ClickHouse{
		Database: "nexora", Table: "querylog",
		WriterUser: "nexora_writer", WriterPassword: "writer-e2e", ReaderUser: "nexora_reader",
		ReaderPasswordFile: filepath.Join(dir, "reader-password"),
	}
	users := strings.NewReplacer("{{WRITER}}", ch.WriterPassword, "{{READER}}", reader).Replace(clickHouseNexoraUsers)
	for path, content := range map[string]string{
		ch.ReaderPasswordFile:                       reader,
		filepath.Join(dir, "users.xml"):             clickHouseUsers,
		filepath.Join(dir, "users.d", "nexora.xml"): users,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			e.T.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			e.T.Fatal(err)
		}
	}
	var tcp int
	for attempt := 1; ; attempt++ {
		httpPort, tcpPort := e.FreePort(), e.FreePort()
		ch.HTTPURL = fmt.Sprintf("http://127.0.0.1:%d", httpPort)
		ch.NativeAddr = fmt.Sprintf("127.0.0.1:%d", tcpPort)
		tcp = tcpPort
		cfg := strings.NewReplacer("{{DIR}}", dir, "{{HTTP}}", fmt.Sprint(httpPort), "{{TCP}}", fmt.Sprint(tcpPort)).Replace(clickHouseConfig)
		cfgPath := filepath.Join(dir, "config.xml")
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
			e.T.Fatal(err)
		}
		ch.Proc = e.Start("clickhouse", []string{"server", "--config-file=" + cfgPath}, nil)
		if ch.waitReady(e, dir, attempt < portAttempts) {
			break
		}
	}
	sql := filepath.Join(repoRoot(), "deploy", "clickhouse", "querylog.sql")
	for range 2 {
		// The command is the toolbox clickhouse binary with harness-built arguments.
		cmd := exec.Command("clickhouse", "client", "--host", "127.0.0.1", "--port", fmt.Sprint(tcp), "--multiquery", "--queries-file", sql) // nosemgrep: dangerous-exec-command
		if out, err := cmd.CombinedOutput(); err != nil {
			e.T.Fatalf("apply %s: %v\n%s", sql, err, out)
		}
	}
	return ch
}

// waitReady waits up to 30 s for GET /ping to answer Ok. When mayRetry is set and the server exited
// because a port was taken, it returns false instead of failing the test.
func (ch *ClickHouse) waitReady(e *Env, dir string, mayRetry bool) bool {
	e.T.Helper()
	logs := func() string {
		a, _ := os.ReadFile(filepath.Join(dir, "clickhouse.err.log"))
		b, _ := os.ReadFile(ch.Proc.LogPath)
		return string(a) + string(b)
	}
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-ch.Proc.done:
			if mayRetry && clickHouseAddrInUse.MatchString(logs()) {
				return false
			}
			e.T.Fatalf("clickhouse server exited:\n%s", tailString(logs(), 50))
		default:
		}
		if resp, err := client.Get(ch.HTTPURL + "/ping"); err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if strings.TrimSpace(string(body)) == "Ok." {
				return true
			}
		}
		if time.Now().After(deadline) {
			e.T.Fatalf("clickhouse not ready within 30s:\n%s", tailString(logs(), 50))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func randomHex(e *Env, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		e.T.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func tailString(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(all[max(0, len(all)-lines):], "\n")
}
