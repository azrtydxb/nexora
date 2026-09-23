//go:build linux

package lease

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func boundedFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(b) > 4096 {
		return "", errors.New("identity file unreadable/oversized")
	}
	return string(b), nil
}
func clockDomain(boot, namespace string) error {
	b, e := boundedFile("/proc/sys/kernel/random/boot_id")
	if e != nil || strings.TrimSpace(b) != boot || boot == "" {
		return errors.New("boot identity mismatch")
	}
	// timens_offsets is a process-level procfs entry, not a per-thread entry.
	// Bind the leader's offset file to the current thread's exact time domain;
	// do not silently read another thread's or future child's namespace offsets.
	for _, prefix := range []string{"/proc/thread-self/ns/", "/proc/self/ns/"} {
		for _, n := range []string{"time", "time_for_children"} {
			s, e := os.Readlink(prefix + n)
			if e != nil || s != namespace || namespace == "" {
				return errors.New("time namespace replaced or unknown")
			}
		}
	}
	s, e := boundedFile("/proc/self/timens_offsets")
	if e != nil {
		return errors.New("time offsets unavailable")
	}
	return zeroOffsets(s)
}
func NewBootClock(bootID, timeNamespace string) (*BootClock, error) {
	c := &BootClock{boot: bootID, namespace: timeNamespace}
	if c.Now() == 0 {
		return nil, c.Err()
	}
	return c, nil
}
func (c *BootClock) Now() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if c.err = clockDomain(c.boot, c.namespace); c.err != nil {
		return 0
	}
	var ts unix.Timespec
	if c.err = unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); c.err != nil {
		return 0
	}
	if c.err = clockDomain(c.boot, c.namespace); c.err != nil {
		return 0
	}
	if ts.Sec < 0 || ts.Nsec < 0 || ts.Nsec >= 1e9 || uint64(ts.Sec) > (math.MaxUint64-uint64(ts.Nsec))/1e9 {
		c.err = errors.New("invalid boottime range")
		return 0
	}
	n := uint64(ts.Sec)*1e9 + uint64(ts.Nsec)
	if n == 0 || n < c.last {
		c.err = errors.New("boottime regressed")
		return 0
	}
	c.last = n
	return n
}

func trustedPath(path string, uid uint32, executable bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("absolute canonical trusted path required")
	}
	for p := path; ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil {
			return errors.New("trusted path unavailable")
		}
		var stat unix.Stat_t
		if unix.Lstat(p, &stat) != nil {
			return errors.New("trusted path stat failed")
		}
		if s.Mode()&os.ModeSymlink != 0 || s.Mode().Perm()&0022 != 0 || (stat.Uid != uid && stat.Uid != 0) {
			return errors.New("mutable or foreign trusted path")
		}
		if p == path {
			if !s.Mode().IsRegular() || stat.Uid != uid || (executable && s.Mode().Perm()&0111 == 0) {
				return errors.New("invalid trusted file")
			}
		} else if !s.IsDir() {
			return errors.New("invalid trusted parent")
		}
		if p == "/" {
			break
		}
	}
	return nil
}
func validAssignment(iface, alias string) bool {
	if len(iface) == 0 || len(iface) > 15 || iface == "." || iface == ".." || len(alias) < 29 || len(alias) > 200 || !strings.HasPrefix(alias, "nexora-fence:") {
		return false
	}
	for _, s := range []string{iface, strings.TrimPrefix(alias, "nexora-fence:")} {
		for _, c := range s {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
				return false
			}
		}
	}
	return true
}

// LaunchDriver executes only inside the calling thread's already-owned netns.
// The trusted driver independently checks namespace, interface alias/type/down,
// foreign hooks and fresh object/map ABI before classic TC attachment.
func LaunchDriver(ctx context.Context, o DriverOptions) (*DriverGate, *BootClock, error) {
	if o.UID != 0 || !o.Enabled || o.IOTimeout <= 0 || o.IOTimeout > time.Minute || o.ReapTimeout <= 0 || o.ReapTimeout > time.Minute || !validAssignment(o.Interface, o.Alias) || uint32(os.Geteuid()) != o.UID || uint32(os.Getuid()) != o.UID {
		return nil, nil, errors.New("invalid explicit driver configuration")
	}
	if e := trustedPath(o.DriverPath, 0, true); e != nil {
		return nil, nil, e
	}
	if e := trustedPath(o.ObjectPath, 0, false); e != nil {
		return nil, nil, e
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	netns, e := os.Readlink("/proc/thread-self/ns/net")
	init, ie := os.Readlink("/proc/1/ns/net")
	if e != nil || ie != nil || netns == init || netns != o.NetworkNamespace {
		return nil, nil, errors.New("owned inherited network namespace required")
	}
	c, e := NewBootClock(o.BootID, o.TimeNamespace)
	if e != nil {
		return nil, nil, e
	}
	dual := o.LabBackendInterface != "" || o.LabBackendAlias != ""
	args := []string{o.Interface, o.Alias, o.ObjectPath}
	if dual {
		if !validAssignment(o.LabBackendInterface, o.LabBackendAlias) || o.Interface == o.LabBackendInterface || o.Alias == o.LabBackendAlias {
			return nil, nil, errors.New("invalid dual lab assignment")
		}
		args = []string{"--isolated-dual-lab", o.Interface, o.Alias, o.LabBackendInterface, o.LabBackendAlias, o.ObjectPath}
	}
	// The driver path is root-owned (trusted) and every argument is validated above.
	cmd := exec.Command(o.DriverPath, args...) // nosemgrep: dangerous-exec-command
	cmd.Env = []string{}
	cmd.Dir = "/"
	g, e := startPrivate(ctx, cmd, o.IOTimeout, o.ReapTimeout, dual)
	if e != nil {
		return nil, nil, e
	}
	// Check both the caller's current thread and the owned child before each
	// exchange. Exclusive namespace administration and an unmodified trusted
	// driver remain required; procfs sampling is not an atomic kernel attestation.
	g.verify = func() error {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		select {
		case <-g.done:
			return ErrGateUncertain
		default:
		}
		current, err := os.Readlink("/proc/thread-self/ns/net")
		if err != nil || current != netns || c.Now() == 0 {
			return ErrGateUncertain
		}
		for _, pair := range [][2]string{{"net", netns}, {"time", o.TimeNamespace}, {"time_for_children", o.TimeNamespace}} {
			actual, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/%s", g.process.Pid, pair[0]))
			if err != nil || actual != pair[1] {
				return ErrGateUncertain
			}
		}
		return nil
	}
	if e = g.verify(); e != nil {
		g.poison()
		return nil, nil, errors.Join(e, g.reaped())
	}
	g.clock = c
	return g, c, nil
}
