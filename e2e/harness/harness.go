// Package harness starts real processes and services for Nexora's Go tests.
package harness

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Env is one test's harness environment.
type Env struct {
	T   *testing.T
	Dir string

	mu    sync.Mutex
	procs []*Proc
}

// New creates an environment rooted in a fresh temporary directory. Every process started through
// it is stopped (in reverse start order) when the test ends; on failure the tail of each log is
// printed.
func New(t *testing.T) *Env {
	t.Helper()
	e := &Env{T: t, Dir: t.TempDir()}
	t.Cleanup(func() {
		e.mu.Lock()
		procs := append([]*Proc(nil), e.procs...)
		e.mu.Unlock()
		for i := len(procs) - 1; i >= 0; i-- {
			procs[i].Stop()
		}
		if t.Failed() {
			for _, p := range procs {
				t.Logf("---- %s log (last 200 lines) ----\n%s", p.Name, tail(p.LogPath, 200))
			}
		}
	})
	return e
}

// Eventually retries cond every 100 ms until it returns nil, failing the test with the last
// error when timeout elapses.
func Eventually(t *testing.T, timeout time.Duration, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := cond()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s: %v", timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// FreePort returns a 127.0.0.1 port that is currently free for both TCP and UDP.
func (e *Env) FreePort() int {
	e.T.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			e.T.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		_ = l.Close()
		if err == nil {
			_ = u.Close()
			return port
		}
	}
	e.T.Fatal("no free TCP+UDP port found")
	return 0
}

// Bin returns the path of a built binary, looking in NEXORA_E2E_BIN_DIR, <repo>/bin,
// <repo>/target/release and $CARGO_TARGET_DIR/release.
func (e *Env) Bin(name string) string {
	e.T.Helper()
	var dirs []string
	if d := os.Getenv("NEXORA_E2E_BIN_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	root := repoRoot()
	dirs = append(dirs, filepath.Join(root, "bin"), filepath.Join(root, "target", "release"))
	if d := os.Getenv("CARGO_TARGET_DIR"); d != "" {
		dirs = append(dirs, filepath.Join(d, "release"))
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	e.T.Fatalf("binary %s not found in %s (run make e2e-build)", name, strings.Join(dirs, ", "))
	return ""
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Proc is a process started by the harness.
type Proc struct {
	Name    string
	Cmd     *exec.Cmd
	LogPath string

	t       *testing.T
	done    chan struct{}
	stopped sync.Once
}

// Start runs the binary `name` (resolved with Bin) with args and extra env, logging stdout and
// stderr to <Dir>/<name>-<n>.log.
func (e *Env) Start(name string, args, env []string) *Proc {
	e.T.Helper()
	bin := e.Bin(name)
	e.mu.Lock()
	n := len(e.procs)
	e.mu.Unlock()
	logPath := filepath.Join(e.Dir, fmt.Sprintf("%s-%d.log", name, n))
	logFile, err := os.Create(logPath)
	if err != nil {
		e.T.Fatal(err)
	}
	// bin comes from Bin (the harness's own build output directories) and name from test code.
	cmd := exec.Command(bin, args...) // nosemgrep: dangerous-exec-command
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		e.T.Fatalf("start %s: %v", name, err)
	}
	p := &Proc{Name: name, Cmd: cmd, LogPath: logPath, t: e.T, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(p.done)
	}()
	e.mu.Lock()
	e.procs = append(e.procs, p)
	e.mu.Unlock()
	return p
}

// Stop sends SIGTERM to the process group, waits up to 5 s, then SIGKILLs it.
func (p *Proc) Stop() {
	p.stopped.Do(func() {
		_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
	})
}

// Kill SIGKILLs the process group and waits for the process to exit.
func (p *Proc) Kill() {
	p.stopped.Do(func() {
		_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	})
}

// Signal delivers sig to the process.
func (p *Proc) Signal(sig os.Signal) {
	_ = p.Cmd.Process.Signal(sig)
}

// WaitLog polls the log every 50 ms until a line matches re and returns that line's submatches;
// it fails the test after timeout or when the process exits first.
func (p *Proc) WaitLog(re *regexp.Regexp, timeout time.Duration) []string {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		exited := false
		select {
		case <-p.done:
			exited = true
		default:
		}
		data, _ := os.ReadFile(p.LogPath)
		for _, line := range bytes.Split(data, []byte("\n")) {
			if m := re.FindSubmatch(line); m != nil {
				out := make([]string, len(m))
				for i := range m {
					out[i] = string(m[i])
				}
				return out
			}
		}
		if exited || time.Now().After(deadline) {
			p.t.Fatalf("%s: no log line matching %q (exited=%v)\n%s", p.Name, re, exited, tail(p.LogPath, 50))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func tail(path string, lines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	return strings.Join(all[max(0, len(all)-lines):], "\n")
}
