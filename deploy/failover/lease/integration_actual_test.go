//go:build linux

package lease_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/piwi3910/nexora/deploy/failover/lease"
	labruntime "github.com/piwi3910/nexora/deploy/failover/runtime"
)

// This executable lab deliberately sends only synthetic Ethernet frames on
// addressless private veth pairs. It is not DR forwarding or HA acceptance.
// Parent provisions the namespace, trusted code, real API server and Lease.
func TestIntegrationActualDriverAPI(t *testing.T) {
	if os.Getenv("NEXORA_FG05_REAL_API") != "yes" {
		t.Skip("parent-only opt-in actual driver/API-server lab")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Geteuid() != 0 {
		t.Fatal("opted-in lab requires root in the parent-owned isolated namespace")
	}
	cfg, err := labruntime.LoadConfig(os.Getenv("NEXORA_FG05_LAB_CONFIG"))
	if err != nil {
		t.Fatalf("explicit lab configuration refused: %v", err)
	}
	if cfg.Driver.Interface != "fg0" || cfg.Driver.UID != 0 || cfg.Stage.UID != 0 {
		t.Fatal("exact root-owned lab fixture required")
	}
	u, err := url.Parse(cfg.Authority.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() != "127.0.0.1" {
		t.Fatal("API server must be the explicitly configured private IPv4 loopback origin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	gateIndex := labActualInventory(t, ctx, cfg.Driver.Alias)
	ownedDown := func(parent context.Context) {
		t.Helper()
		ns, err := os.Readlink("/proc/self/ns/net")
		if err != nil || ns != cfg.Driver.NetworkNamespace {
			t.Fatal("cleanup namespace changed; retain evidence")
		}
		var links []struct {
			Index int    `json:"ifindex"`
			Alias string `json:"ifalias"`
		}
		if err := json.Unmarshal(labActualCommand(t, parent, "/usr/sbin/ip", "-j", "link", "show", "dev", "fg0"), &links); err != nil || len(links) != 1 || links[0].Index != gateIndex || links[0].Alias != cfg.Driver.Alias {
			t.Fatal("cleanup link ownership changed; retain evidence")
		}
		labActualCommand(t, parent, "/usr/sbin/ip", "link", "set", "dev", "fg0", "down")
	}
	python := cfg.Stage.Python
	probe := filepath.Clean(filepath.Join(filepath.Dir(cfg.Stage.Script), "../../fence/smoke.py"))
	labActualTrusted(t, python, true)
	labActualTrusted(t, probe, false)
	packet := func(mode string) {
		t.Helper()
		t.Logf("packet probe %s:\n%s", mode, labActualCommand(t, ctx, python, "-I", "-B", probe, "--packet-probe", mode))
	}
	authority, err := lease.NewHTTPS(cfg.Authority)
	if err != nil {
		t.Fatal(err)
	}
	old, err := authority.Get(ctx)
	if err != nil {
		t.Fatal("real API GET failed")
	}
	change := func(record lease.Record) lease.Record {
		t.Helper()
		next := record
		next.Holder, err = lease.NewHolderID()
		if err != nil {
			t.Fatal(err)
		}
		next.Nonce, err = lease.NewHolderID()
		if err != nil {
			t.Fatal(err)
		}
		next.Epoch++
		return next
	}
	next := change(old)
	ack, err := authority.CAS(ctx, old, next)
	if err != nil || ack.RV == old.RV {
		t.Fatal("real API CAS did not acknowledge a new resourceVersion")
	}
	if _, err = authority.CAS(ctx, old, change(old)); err == nil {
		t.Fatal("real API accepted stale resourceVersion")
	}
	current, err := authority.Get(ctx)
	if err != nil || current.RV != ack.RV || current.Nonce != ack.Nonce {
		t.Fatal("stale CAS changed the real Lease")
	}
	gate, clock, err := lease.LaunchDriver(ctx, cfg.Driver)
	if err != nil {
		t.Fatalf("actual driver launch failed: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			shutdown, stop := context.WithTimeout(context.Background(), cfg.Driver.IOTimeout+cfg.Driver.ReapTimeout)
			defer stop()
			if err := gate.Close(shutdown); err != nil {
				t.Errorf("driver shutdown uncertainty: %v", err)
			}
		}
		// Retain the hook and exact namespace. Parent owns namespace cleanup.
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		ownedDown(shutdown)
	})
	// LaunchDriver returned only after the real denying READY acknowledgement.
	labActualCommand(t, ctx, "/usr/sbin/ip", "link", "set", "dev", "fg0", "up")
	packet("deny")
	holder, err := lease.NewHolderID()
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := lease.New(authority, gate, clock, lease.Config{Holder: holder, Margin: cfg.Margin, IOTimeout: cfg.Driver.IOTimeout})
	if err != nil {
		t.Fatal(err)
	}
	first, err := ctl.Step(ctx)
	if err != nil || first.CASAcknowledged || first.TicketArmed {
		t.Fatal("first real observation did not quarantine")
	}
	start := clock.Now()
	if start == 0 {
		t.Fatal("invalid actual BootClock")
	}
	waitUntil := func(deadline uint64) {
		t.Helper()
		for {
			now := clock.Now()
			if now == 0 {
				t.Fatal("actual BootClock failed")
			}
			if now >= deadline {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("bounded lab deadline expired")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	waitUntil(start + uint64(lease.MaxKernelWindow+cfg.Margin))
	armed, err := ctl.Step(ctx)
	if err != nil || !armed.CASAcknowledged || !armed.TicketArmed {
		t.Fatalf("real quarantined acquisition/ARM failed: %v", err)
	}
	packet("allow")
	// No renewal occurs. The actual kernel, not a userspace timer, must deny.
	waitUntil(armed.Ticket.Deadline)
	packet("deny")
	current, err = authority.Get(ctx)
	if err != nil {
		t.Fatal("real API read before ownership change failed")
	}
	if _, err = authority.CAS(ctx, current, change(current)); err != nil {
		t.Fatal("real API ownership change failed")
	}
	lost, err := ctl.Step(ctx)
	if err != nil || lost.CASAcknowledged || lost.TicketArmed {
		t.Fatal("observed ownership change must yield nil-error quarantine without ARM")
	}
	packet("deny")
	canceled, stop := context.WithCancel(ctx)
	stop()
	if result, err := ctl.Step(canceled); err == nil || result.TicketArmed {
		t.Fatal("cancelled real authority operation was accepted")
	}
	packet("deny")
	// Deliberate negative API misuse, after ending all controller operations:
	// consumed original authorization cannot be replayed, even after expiry.
	if err = gate.Arm(ctx, armed.Ticket); !errors.Is(err, lease.ErrGateUncertain) {
		t.Fatal("stale original ARM did not permanently reject the connection")
	}
	shutdown, stopShutdown := context.WithTimeout(context.Background(), cfg.Driver.IOTimeout+cfg.Driver.ReapTimeout)
	closeErr := gate.Close(shutdown)
	stopShutdown()
	closed = true
	if closeErr == nil {
		t.Fatal("poisoned Close concealed enforcement uncertainty")
	}
	packet("deny")
	ownedDown(ctx)
	if replacement, _, err := lease.LaunchDriver(ctx, cfg.Driver); err == nil {
		shutdown, stop := context.WithTimeout(context.Background(), cfg.Driver.IOTimeout+cfg.Driver.ReapTimeout)
		defer stop()
		_ = replacement.Close(shutdown)
		t.Fatal("restart unexpectedly reused an existing kernel hook")
	}
	t.Log("real API CAS, actual binding, quarantine/ARM, ARP/GARP/data expiry, independent management, ownership loss, cancellation and restart refusal passed; no DR/HA acceptance")
}

// Bound both subprocess time and retained output; never invoke a shell.
type labActualOutput struct{ bytes.Buffer }

func (b *labActualOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}
func labActualCommand(t *testing.T, parent context.Context, executable string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "PYTHONDONTWRITEBYTECODE=1"}
	cmd.WaitDelay = time.Second
	var out labActualOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("bounded lab command %s failed: %v\n%s", filepath.Base(executable), err, out.String())
	}
	return out.Bytes()
}

func labActualTrusted(t *testing.T, path string, executable bool) {
	t.Helper()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatal("canonical trusted lab code path required")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal("trusted lab code unavailable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			t.Fatal("mutable/foreign lab code refused")
		}
		if p == path {
			if !info.Mode().IsRegular() || (executable && info.Mode().Perm()&0111 == 0) {
				t.Fatal("invalid lab executable/source")
			}
		} else if !info.IsDir() {
			t.Fatal("invalid lab code ancestor")
		}
		if p == "/" {
			return
		}
	}
}

func labActualPeer(index int, name string, byName map[string]int) (int, error) {
	if index < 0 {
		return 0, errors.New("invalid peer index")
	}
	if name == "" {
		return index, nil
	}
	resolved := byName[name]
	if resolved == 0 || (index != 0 && index != resolved) {
		return 0, errors.New("unknown or conflicting peer identity")
	}
	return resolved, nil
}

func TestActualLocalPeerIdentity(t *testing.T) {
	for _, tc := range []struct {
		index   int
		name    string
		want    int
		invalid bool
	}{
		{0, "fp0", 3, false}, {3, "", 3, false}, {3, "fp0", 3, false},
		{2, "fp0", 0, true}, {0, "foreign", 0, true}, {-1, "fp0", 0, true},
	} {
		got, err := labActualPeer(tc.index, tc.name, map[string]int{"fg0": 2, "fp0": 3})
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("peer %+v: %d %v", tc, got, err)
		}
	}
}

func labActualInventory(t *testing.T, ctx context.Context, alias string) int {
	t.Helper()
	gateIndex := 0
	var links []struct {
		Index         int             `json:"ifindex"`
		Peer          int             `json:"link_index"`
		PeerName      string          `json:"link"`
		PeerNamespace json.RawMessage `json:"link_netnsid"`
		Master        json.RawMessage `json:"master"`
		LinkType      string          `json:"link_type"`
		Name          string          `json:"ifname"`
		Alias         string          `json:"ifalias"`
		Flags         []string        `json:"flags"`
		Info          struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
		Addresses []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
			Prefix int    `json:"prefixlen"`
			Scope  string `json:"scope"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(labActualCommand(t, ctx, "/usr/sbin/ip", "-j", "-d", "address", "show"), &links); err != nil {
		t.Fatal("invalid lab link inventory")
	}
	expected := map[string]bool{"lo": true, "fg0": false, "fp0": true, "mg0": true, "mp0": true}
	if len(links) != len(expected) {
		t.Fatal("unexpected lab transmission path")
	}
	indices := map[int]bool{}
	byName := map[string]int{}
	peers := map[string]int{}
	for _, link := range links {
		up, ok := expected[link.Name]
		if !ok {
			t.Fatal("unexpected or duplicate lab attachment")
		}
		delete(expected, link.Name)
		if link.Index <= 0 || indices[link.Index] || len(link.PeerNamespace) != 0 || len(link.Master) != 0 {
			t.Fatal("foreign namespace, master or invalid/duplicate interface index")
		}
		indices[link.Index] = true
		byName[link.Name], peers[link.Name] = link.Index, link.Peer
		isUp := false
		for _, f := range link.Flags {
			isUp = isUp || f == "UP"
		}
		if isUp != up {
			t.Fatal("incorrect provisioned link state")
		}
		if link.Name != "lo" && (link.Info.Kind != "veth" || len(link.Addresses) != 0) {
			t.Fatal("only addressless lab veths are permitted")
		}
		if link.Name == "lo" {
			if link.LinkType != "loopback" {
				t.Fatal("lo is not loopback")
			}
			seen := map[string]bool{}
			for _, address := range link.Addresses {
				valid4 := address.Family == "inet" && address.Local == "127.0.0.1" && address.Prefix == 8
				valid6 := address.Family == "inet6" && address.Local == "::1" && address.Prefix == 128
				if (!valid4 && !valid6) || address.Scope != "host" || seen[address.Local] {
					t.Fatal("unexpected or duplicate loopback address")
				}
				seen[address.Local] = true
			}
			if !seen["127.0.0.1"] {
				t.Fatal("private API loopback address missing")
			}
		}
		if link.Name == "fg0" {
			if link.Alias != alias || link.Index <= 0 {
				t.Fatal("lab gate ownership mismatch")
			}
			gateIndex = link.Index
		}
	}
	for _, link := range links {
		peer, err := labActualPeer(link.Peer, link.PeerName, byName)
		if err != nil {
			t.Fatal(err)
		}
		peers[link.Name] = peer
	}
	for _, pair := range [][2]string{{"fg0", "fp0"}, {"mg0", "mp0"}} {
		if peers[pair[0]] != byName[pair[1]] || peers[pair[1]] != byName[pair[0]] {
			t.Fatal("lab veth peers are not the exact reciprocal local attachments")
		}
	}
	for _, family := range []string{"-4", "-6"} {
		var routes []struct {
			Dev string `json:"dev"`
		}
		if err := json.Unmarshal(labActualCommand(t, ctx, "/usr/sbin/ip", "-j", family, "route", "show", "table", "all"), &routes); err != nil {
			t.Fatal("invalid lab route inventory")
		}
		for _, route := range routes {
			if route.Dev != "lo" {
				t.Fatal("lab has non-loopback route")
			}
		}
	}
	for _, dev := range []string{"all", "default", "fg0", "fp0", "mg0", "mp0"} {
		data, err := os.ReadFile("/proc/sys/net/ipv6/conf/" + dev + "/disable_ipv6")
		if err != nil || strings.TrimSpace(string(data)) != "1" {
			t.Fatal("IPv6 must already be disabled on lab transmission paths")
		}
	}
	return gateIndex
}
