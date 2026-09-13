package harness

import (
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
	port := freeTCPPort(t)
	pg := &Postgres{Dir: dir, Port: port, URL: fmt.Sprintf("postgres://nexora@127.0.0.1:%d/nexora?sslmode=disable", port)}
	runPG(t, cred, "initdb", "-D", dir, "-U", "nexora", "--auth=trust", "-E", "UTF8")
	runPG(t, cred, "pg_ctl", "-D", dir, "-o",
		fmt.Sprintf("-p %d -k %s -c listen_addresses=127.0.0.1 -c fsync=off", port, dir),
		"-l", filepath.Join(dir, "log"), "start", "-w")
	t.Cleanup(func() { stopPG(pg, cred) })
	runPG(t, cred, "createdb", "-h", "127.0.0.1", "-p", strconv.Itoa(port), "-U", "nexora", "nexora")
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

func runPG(t *testing.T, cred *syscall.Credential, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = os.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	if out, err := cmd.CombinedOutput(); err != nil {
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
