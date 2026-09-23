//go:build linux

package lease

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Local validation tests only; no namespaces, links, BPF or drivers are created.
func TestLinuxBindingRefusals(t *testing.T) {
	for _, p := range [][2]string{{"eth0", "nexora-fence:0123456789abcdef"}, {"veth-test", "nexora-fence:abc_def-01234567"}} {
		if !validAssignment(p[0], p[1]) {
			t.Fatal(p)
		}
	}
	for _, p := range [][2]string{{"", "nexora-fence:0123456789abcdef"}, {"../eth0", "nexora-fence:0123456789abcdef"}, {"eth 0", "nexora-fence:0123456789abcdef"}, {"eth0", "wrong:0123456789abcdef"}, {"eth0", "nexora-fence:short"}} {
		if validAssignment(p[0], p[1]) {
			t.Fatal(p)
		}
	}
	if g, c, e := LaunchDriver(context.Background(), DriverOptions{}); e == nil || g != nil || c != nil {
		t.Fatal("implicit launch")
	}
	if c, e := NewBootClock("wrong-boot", "time:[1]"); e == nil || c != nil {
		t.Fatal("accepted wrong boot")
	}
	b, e := boundedFile("/proc/sys/kernel/random/boot_id")
	if e == nil {
		if c, e := NewBootClock(strings.TrimSpace(b), "time:[1]"); e == nil || c != nil {
			t.Fatal("accepted wrong namespace")
		}
	}
	c := &BootClock{boot: "wrong-boot", namespace: "time:[1]"}
	if c.Now() != 0 || c.Err() == nil || c.Now() != 0 {
		t.Fatal("failure not latched")
	}
	for _, p := range []string{"driver", "/tmp/../driver", "/missing-lease-binding-driver"} {
		if trustedPath(p, uint32(os.Getuid()), true) == nil {
			t.Fatal(p)
		}
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "mutable")
	if e = os.WriteFile(p, []byte("test"), 0777); e != nil {
		t.Fatal(e)
	}
	if trustedPath(p, uint32(os.Getuid()), true) == nil {
		t.Fatal("mutable path")
	}
	link := filepath.Join(dir, "link")
	if e = os.Symlink(p, link); e != nil {
		t.Fatal(e)
	}
	if trustedPath(link, uint32(os.Getuid()), true) == nil {
		t.Fatal("symlink path")
	}
}
func TestLinuxClockAbsolute(t *testing.T) {
	boot, e := boundedFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		t.Skip("proc boot unavailable")
	}
	ns, e := os.Readlink("/proc/thread-self/ns/time")
	if e != nil {
		t.Skip("time namespace unavailable")
	}
	offsets, e := boundedFile("/proc/self/timens_offsets")
	if e != nil || zeroOffsets(offsets) != nil {
		t.Skip("process offset file unavailable or nonzero")
	}
	c, e := NewBootClock(strings.TrimSpace(boot), ns)
	if e != nil {
		t.Fatalf("available zero-offset domain rejected: %v", e)
	}
	before := c.Now()
	time.Sleep(time.Millisecond)
	var raw unix.Timespec
	if e := unix.ClockGettime(unix.CLOCK_BOOTTIME, &raw); e != nil {
		t.Fatal(e)
	}
	sampled := uint64(raw.Sec)*1e9 + uint64(raw.Nsec)
	after := c.Now()
	if before == 0 || before > sampled || sampled > after || before < uint64(time.Second) {
		t.Fatal("not the actual absolute kernel boottime domain")
	}
	c.namespace = "time:[1]"
	if c.Now() != 0 || c.Err() == nil {
		t.Fatal("namespace change not refused")
	}
}
