package harness

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// netLabEnv marks a process re-executed inside the lab's user and network namespace.
const netLabEnv = "NEXORA_NETLAB"

// InNetLab reports whether this process runs inside the lab (NEXORA_NETLAB=1).
func InNetLab() bool { return os.Getenv(netLabEnv) == "1" }

// RunInNetLab re-executes the current test (and only it) inside `unshare -Urn --kill-child`
// and fails t when the inner run fails; the caller returns right after. Inside, the test is root
// of a private user namespace with its own network stack, so it can create namespaces and veth
// links and open multicast sockets without touching the pod's network.
func RunInNetLab(t *testing.T) {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare is not in PATH; the network namespace lab runs in the dev pod")
	}
	// os.Args[0] is this test binary; the name comes from the running test.
	cmd := exec.Command(unshare, "-Urn", "--kill-child", "--", os.Args[0], // nosemgrep: dangerous-exec-command
		"-test.run", "^"+regexp.QuoteMeta(t.Name())+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), netLabEnv+"=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the network namespace lab: %v", err)
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var log []string
	for sc.Scan() {
		log = append(log, sc.Text())
		t.Log(sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		// unshare itself refused: the container lacks the privileges the lab needs (CI runners
		// have no CAP_SYS_ADMIN and no user namespaces). The dev pod does, and runs it there.
		for _, l := range log {
			if strings.Contains(l, "unshare failed") || strings.Contains(l, "Operation not permitted") {
				t.Skipf("the network namespace lab needs privileges this container lacks: %s", l)
			}
		}
		t.Fatalf("test inside the network namespace lab failed: %v", err)
	}
}

// NetLab is a set of network namespaces joined by veth links. The lab's root namespace (where
// the test runs) is the "gateway" namespace; each child namespace is held open by a sleeping
// process.
type NetLab struct {
	e   *Env
	pid map[string]int
}

// NewNetLab brings up lo in the lab's root namespace (the "gateway" namespace).
func (e *Env) NewNetLab() *NetLab {
	e.T.Helper()
	if !InNetLab() {
		e.T.Fatal("NewNetLab outside the network namespace lab: call RunInNetLab first")
	}
	l := &NetLab{e: e, pid: map[string]int{}}
	l.run("ip", "link", "set", "lo", "up")
	return l
}

// Namespace creates (once) a child network namespace named name, held by a sleeping process.
func (l *NetLab) Namespace(name string) int {
	l.e.T.Helper()
	if pid, ok := l.pid[name]; ok {
		return pid
	}
	// unshare execs sleep without forking, so the child's pid is the namespace holder.
	cmd := exec.Command("unshare", "-n", "--", "sleep", "infinity")
	if err := cmd.Start(); err != nil {
		l.e.T.Fatalf("namespace %s: %v", name, err)
	}
	pid := cmd.Process.Pid
	l.e.T.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	// The holder may not have unshared yet; wait until its network namespace differs from ours.
	Eventually(l.e.T, 5*time.Second, func() error {
		self, err := os.Readlink("/proc/self/ns/net")
		if err != nil {
			return err
		}
		child, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid))
		if err != nil {
			return err
		}
		if child == self {
			return fmt.Errorf("namespace %s: holder %d has not unshared yet", name, pid)
		}
		return nil
	})
	l.pid[name] = pid
	l.runIn(name, "ip", "link", "set", "lo", "up")
	return pid
}

// Link creates veth local<->peer, moves peer into namespace ns, sets 10.254.<subnet>.1/24 on local
// and .2/24 on peer, multicast on, both up; it returns the two IPv4 addresses.
func (l *NetLab) Link(local, peer, ns string, subnet int) (localIP, peerIP netip.Addr) {
	l.e.T.Helper()
	if subnet < 0 || subnet > 255 {
		l.e.T.Fatalf("link %s: subnet %d out of range", local, subnet)
	}
	pid := l.Namespace(ns)
	localIP = netip.AddrFrom4([4]byte{10, 254, byte(subnet), 1})
	peerIP = netip.AddrFrom4([4]byte{10, 254, byte(subnet), 2})
	l.run("ip", "link", "add", local, "type", "veth", "peer", "name", peer)
	l.run("ip", "link", "set", peer, "netns", strconv.Itoa(pid))
	l.run("ip", "addr", "add", localIP.String()+"/24", "dev", local)
	l.run("ip", "link", "set", local, "multicast", "on", "up")
	l.runIn(ns, "ip", "addr", "add", peerIP.String()+"/24", "dev", peer)
	l.runIn(ns, "ip", "link", "set", peer, "multicast", "on", "up")
	// A multicast sender in the namespace needs a route even when it names no interface.
	l.runIn(ns, "ip", "route", "add", "224.0.0.0/4", "dev", peer)
	return localIP, peerIP
}

// StartIn runs a harness binary inside namespace ns (nsenter -t <pid> -n) and waits for its READY line.
func (l *NetLab) StartIn(ns, bin string, args ...string) *Proc {
	l.e.T.Helper()
	pid := l.Namespace(ns)
	// nsenter without --pid execs the binary in place, so the Proc's pid is the binary's.
	p := l.e.Start("nsenter", append([]string{"-t", strconv.Itoa(pid), "-n", "--", l.e.Bin(bin)}, args...), nil)
	p.Name = bin + "@" + ns
	p.WaitLog(readyLine, 10*time.Second)
	return p
}

// RunIn runs a harness binary to completion inside namespace ns and returns its stdout.
func (l *NetLab) RunIn(ns, bin string, args ...string) string {
	l.e.T.Helper()
	pid := l.Namespace(ns)
	return runOutput(l.e.T, "nsenter", append([]string{"-t", strconv.Itoa(pid), "-n", "--", l.e.Bin(bin)}, args...)...)
}

// RunBin runs a harness binary to completion in the current namespace and returns its stdout.
func (e *Env) RunBin(t *testing.T, bin string, args ...string) string {
	t.Helper()
	return runOutput(t, e.Bin(bin), args...)
}

func (l *NetLab) run(name string, args ...string) {
	l.e.T.Helper()
	runOutput(l.e.T, name, args...)
}

func (l *NetLab) runIn(ns, name string, args ...string) {
	l.e.T.Helper()
	runOutput(l.e.T, "nsenter", append([]string{"-t", strconv.Itoa(l.pid[ns]), "-n", "--", name}, args...)...)
}

// runOutput runs a command from test code to completion and returns its stdout, failing t with
// stderr on a non-zero exit.
func runOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...) // nosemgrep: dangerous-exec-command
	var stderr []byte
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = ee.Stderr
	}
	if err != nil {
		t.Fatalf("%s %v: %v\n%s%s", name, args, err, out, stderr)
	}
	return string(out)
}
