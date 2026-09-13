package harness

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// Postgres is a throwaway PostgreSQL cluster owned by one test.
type Postgres struct {
	URL, Dir string
	Port     int
}

// StartPostgres initialises and starts a PostgreSQL cluster on a free 127.0.0.1 port with a
// `nexora` database; it is stopped when the test ends. As root it runs as user `dev`, because
// PostgreSQL refuses to run as root.
func (e *Env) StartPostgres() *Postgres {
	t := e.T
	t.Helper()
	// The data dir lives in its own world-traversable temp dir: t.TempDir() parents are 0700
	// root-owned, which the unprivileged postgres user cannot enter.
	base, err := os.MkdirTemp("", "nexora-pg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "pg")
	cred := pgCredential(t)
	if cred != nil {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(dir, int(cred.Uid), int(cred.Gid)); err != nil {
			t.Fatal(err)
		}
	}
	runPG(t, cred, "initdb", "-D", dir, "-U", "nexora", "--auth=trust", "-E", "UTF8")
	// PostgreSQL cannot listen on port 0, so a free port is picked and the start retried with
	// another one when something else took it first.
	pg := &Postgres{Dir: dir}
	for attempt := 1; ; attempt++ {
		pg.Port = freeTCPPort(t)
		pg.URL = fmt.Sprintf("postgres://nexora@127.0.0.1:%d/nexora?sslmode=disable", pg.Port)
		logPath := filepath.Join(dir, "log")
		out, err := pgCmd(cred, "pg_ctl", "-D", dir, "-o",
			fmt.Sprintf("-p %d -k %s -c listen_addresses=127.0.0.1 -c fsync=off", pg.Port, dir),
			"-l", logPath, "start", "-w").CombinedOutput()
		if err == nil {
			break
		}
		serverLog, _ := os.ReadFile(logPath)
		if attempt == portAttempts || !bytes.Contains(serverLog, []byte("could not bind")) {
			t.Fatalf("pg_ctl start: %v\n%s\n%s", err, out, serverLog)
		}
		_ = os.Remove(logPath)
	}
	t.Cleanup(func() { stopPG(pg, cred) })
	runPG(t, cred, "createdb", "-h", "127.0.0.1", "-p", strconv.Itoa(pg.Port), "-U", "nexora", "nexora")
	return pg
}

// StopPostgres stops the cluster immediately (simulating a database outage).
func StopPostgres(t *testing.T, pg *Postgres) {
	t.Helper()
	if out, err := stopPG(pg, pgCredential(t)); err != nil {
		t.Fatalf("pg_ctl stop: %v\n%s", err, out)
	}
}

func stopPG(pg *Postgres, cred *syscall.Credential) ([]byte, error) {
	if _, err := os.Stat(filepath.Join(pg.Dir, "postmaster.pid")); err != nil {
		return nil, nil
	}
	cmd := exec.Command("pg_ctl", "-D", pg.Dir, "stop", "-m", "immediate", "-w")
	cmd.Dir = filepath.Dir(pg.Dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	return cmd.CombinedOutput()
}

func pgCredential(t *testing.T) *syscall.Credential {
	t.Helper()
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup("dev")
	if err != nil {
		t.Fatalf("running as root needs user dev for PostgreSQL: %v", err)
	}
	uid, _ := strconv.ParseUint(u.Uid, 10, 32)
	gid, _ := strconv.ParseUint(u.Gid, 10, 32)
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
}

// portAttempts bounds the restarts of a program that cannot listen on port 0 when its pre-picked
// port was taken.
const portAttempts = 10

func pgCmd(cred *syscall.Credential, name string, args ...string) *exec.Cmd {
	// name is a fixed PostgreSQL tool name (initdb, pg_ctl, createdb) from this file.
	cmd := exec.Command(name, args...) // nosemgrep: dangerous-exec-command
	cmd.Dir = os.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	return cmd
}

func runPG(t *testing.T, cred *syscall.Credential, name string, args ...string) {
	t.Helper()
	if out, err := pgCmd(cred, name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, out)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
