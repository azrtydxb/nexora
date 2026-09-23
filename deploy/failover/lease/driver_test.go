package lease

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Only tests can reach this fake process. Production has no fake/command override.
func TestDriverFixture(t *testing.T) {
	args := os.Args
	if len(args) < 3 || args[len(args)-2] != "lease-pipe-fixture" {
		return
	}
	mode := args[len(args)-1]
	if mode == "ready-hang" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if strings.HasPrefix(mode, "ready:") {
		fmt.Println(strings.TrimPrefix(mode, "ready:"))
		os.Exit(0)
	}
	fmt.Println("READY ifindex=2 program=3 horizon_ns=5000000000")
	s := bufio.NewScanner(os.Stdin)
	capture := uint64(10)
	for s.Scan() {
		line := s.Text()
		switch mode {
		case "hang", "write-block":
			time.Sleep(time.Minute)
			os.Exit(0)
		case "late":
			time.Sleep(500 * time.Millisecond)
		case "eof":
			os.Exit(0)
		case "partial":
			fmt.Print("DEN")
			time.Sleep(time.Minute)
			os.Exit(0)
		case "oversized":
			fmt.Println(strings.Repeat("X", 128))
			continue
		case "wrong":
			fmt.Println("ARMED")
			continue
		}
		switch {
		case line == "CAPTURE":
			if strings.HasPrefix(mode, "ticket:") {
				fmt.Println(strings.TrimPrefix(mode, "ticket:"))
			} else {
				fmt.Printf("TICKET %d %d\n", capture, capture+5000000000)
				if mode != "replay" {
					capture++
				}
			}
		case strings.HasPrefix(line, "ARM "):
			if mode == "arm-late" {
				time.Sleep(500 * time.Millisecond)
			}
			fmt.Println("ARMED")
		case line == "DENY":
			fmt.Println("DENIED")
		default:
			fmt.Println("REJECTED")
		}
	}
	os.Exit(0)
}
func pipeFixture(t *testing.T, mode string) *DriverGate {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(exe, "-test.run=^TestDriverFixture$", "lease-pipe-fixture", mode)
	cmd.Env = []string{}
	g, e := startPrivate(context.Background(), cmd, 2*time.Second, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	g.timeout = 150 * time.Millisecond
	t.Cleanup(func() {
		g.poison()
		if e := g.reaped(); e != nil {
			t.Error(e)
		}
	})
	return g
}
func TestDriverRoundTrip(t *testing.T) {
	g := pipeFixture(t, "normal")
	ctx := context.Background()
	if e := g.Deny(ctx); e != nil {
		t.Fatal(e)
	}
	ticket, e := g.Capture(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = g.Arm(ctx, ticket); e != nil {
		t.Fatal(e)
	}
	if e = g.Close(ctx); e != nil {
		t.Fatal(e)
	}
	if !g.poisoned.Load() {
		t.Fatal("close not terminal")
	}
	if e = g.Deny(ctx); !errors.Is(e, ErrGateUncertain) {
		t.Fatal(e)
	}
}
func TestDriverFailures(t *testing.T) {
	for _, mode := range []string{"hang", "late", "eof", "partial", "oversized", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			g := pipeFixture(t, mode)
			start := time.Now()
			if e := g.Deny(context.Background()); !errors.Is(e, ErrGateUncertain) {
				t.Fatal(e)
			}
			if e := g.reaped(); e != nil {
				t.Fatal(e)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("cleanup exceeded bound")
			}
			if e := g.Deny(context.Background()); !errors.Is(e, ErrGateUncertain) {
				t.Fatal("late reply reused", e)
			}
		})
	}
}
func TestDriverMalformedReady(t *testing.T) {
	for _, line := range []string{"READY ifindex=0 program=3 horizon_ns=5000000000", "READY ifindex=02 program=3 horizon_ns=5000000000", "READY ifindex=2147483648 program=3 horizon_ns=5000000000", "READY ifindex=2 program=4294967296 horizon_ns=5000000000", "READY ifindex=2 program=3 horizon_ns=5000000001", "READY  ifindex=2 program=3 horizon_ns=5000000000", "READY ifindex=2 program=3 horizon_ns=5000000000 extra", "READY ifindex=2 program=3 horizon_ns=5000000000\r"} {
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "-test.run=^TestDriverFixture$", "lease-pipe-fixture", "ready:"+line)
		cmd.Env = []string{}
		if g, e := startPrivate(context.Background(), cmd, 2*time.Second, time.Second); e == nil {
			g.poison()
			t.Fatal("accepted", line)
		}
	}
}
func TestDriverMalformedTicket(t *testing.T) {
	for _, line := range []string{"TICKET 0 5000000000", "TICKET 01 5000000001", "TICKET +1 5000000001", "TICKET 1 5000000002", "TICKET 18446744073709551616 5", "TICKET 18446744073709551615 5", "TICKET 1 5000000001 trailing", "TICKET  1 5000000001", "TICKET 1 5000000001\r"} {
		g := pipeFixture(t, "ticket:"+line)
		if _, e := g.Capture(context.Background()); !errors.Is(e, ErrGateUncertain) {
			t.Fatal("accepted", line, e)
		}
	}
}
func TestDriverTicketReplayAndMismatch(t *testing.T) {
	for _, mode := range []string{"replay", "mismatch", "double-capture", "arm-after-deny", "double-arm"} {
		t.Run(mode, func(t *testing.T) {
			g := pipeFixture(t, mode)
			ctx := context.Background()
			ticket, e := g.Capture(ctx)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "replay":
				if e = g.Arm(ctx, ticket); e != nil {
					t.Fatal(e)
				}
				_, e = g.Capture(ctx)
			case "mismatch":
				ticket.Deadline++
				e = g.Arm(ctx, ticket)
			case "double-capture":
				_, e = g.Capture(ctx)
			case "arm-after-deny":
				if e = g.Deny(ctx); e != nil {
					t.Fatal(e)
				}
				e = g.Arm(ctx, ticket)
			case "double-arm":
				if e = g.Arm(ctx, ticket); e != nil {
					t.Fatal(e)
				}
				e = g.Arm(ctx, ticket)
			}
			if !errors.Is(e, ErrGateUncertain) {
				t.Fatal(e)
			}
		})
	}
}
func TestDriverCancellationAndPipeDeadlines(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		g := pipeFixture(t, "hang")
		g.timeout = time.Second
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(25*time.Millisecond, cancel)
		start := time.Now()
		if e := g.Deny(ctx); e == nil {
			t.Fatal("accepted cancellation")
		}
		if time.Since(start) > 500*time.Millisecond {
			t.Fatal("cancellation failed to interrupt read")
		}
	})
	t.Run("blocked-write", func(t *testing.T) {
		g := pipeFixture(t, "write-block")
		start := time.Now()
		e := g.exchange(context.Background(), strings.Repeat("X", 4<<20), func(string) error { return nil })
		if e == nil || time.Since(start) > time.Second {
			t.Fatal("pipe write deadline failed", e)
		}
	})
	t.Run("lock-timeout", func(t *testing.T) {
		g := pipeFixture(t, "normal")
		g.serial <- struct{}{}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if e := g.Deny(ctx); e == nil {
			t.Fatal("lock unbounded")
		}
		<-g.serial
		if !g.poisoned.Load() {
			t.Fatal("not terminal")
		}
	})
	t.Run("startup-timeout", func(t *testing.T) {
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "-test.run=^TestDriverFixture$", "lease-pipe-fixture", "ready-hang")
		cmd.Env = []string{}
		start := time.Now()
		if _, e := startPrivate(context.Background(), cmd, 150*time.Millisecond, time.Second); e == nil {
			t.Fatal("startup accepted")
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("startup cleanup unbounded")
		}
	})
}
func TestZeroOffsets(t *testing.T) {
	if e := zeroOffsets("monotonic           0         0\nboottime            0         0\n"); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"", "boottime 0 0\n", "monotonic 0 0\nboottime 0 1\n", "monotonic -1 0\nboottime 0 0\n", "monotonic 0 0\nboottime 0 0\nboottime 0 0\n", "monotonic 0 0\nboottime 0 0\nunknown 0 0\n", "monotonic 0 0\nboottime 0 0 extra\n"} {
		if zeroOffsets(s) == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestDriverSingleControllerBinding(t *testing.T) {
	g := pipeFixture(t, "normal")
	clock := &BootClock{}
	g.clock = clock
	a := &fakeAuthority{}
	cfg := Config{Holder: strings.Repeat("b", 64), Margin: time.Second, IOTimeout: time.Second}
	if _, e := New(a, (*DriverGate)(nil), clock, cfg); e == nil {
		t.Fatal("accepted nil concrete driver")
	}
	if _, e := New(a, g, &BootClock{}, cfg); e == nil {
		t.Fatal("accepted different clock")
	}
	if _, e := New(a, g, clock, cfg); e != nil {
		t.Fatal(e)
	}
	if _, e := New(a, g, clock, cfg); e == nil {
		t.Fatal("bound twice")
	}
}
func TestDriverIdentityFailure(t *testing.T) {
	g := pipeFixture(t, "normal")
	g.verify = func() error { return errors.New("identity changed") }
	if e := g.Deny(context.Background()); !errors.Is(e, ErrGateUncertain) {
		t.Fatal(e)
	}
	if !g.poisoned.Load() {
		t.Fatal("identity failure not terminal")
	}
}
func TestDriverConcurrentPoison(t *testing.T) {
	g := pipeFixture(t, "hang")
	g.timeout = time.Second
	done := make(chan error, 1)
	go func() { done <- g.Deny(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if e := g.Close(ctx); !errors.Is(e, ErrGateUncertain) {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ErrGateUncertain) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent read did not unblock")
	}
}
func TestDriverReapBound(t *testing.T) {
	// Test the uninterruptible-process reporting branch without creating an
	// unkillable process. The real-child tests above verify actual Wait/reap.
	g := &DriverGate{done: make(chan struct{}), reap: 20 * time.Millisecond}
	start := time.Now()
	if g.reaped() == nil || time.Since(start) > time.Second {
		t.Fatal("reap not bounded")
	}
}

func TestDriverLateArmNeverRecaptures(t *testing.T) {
	g := pipeFixture(t, "arm-late")
	ticket, e := g.Capture(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = g.Arm(context.Background(), ticket); !errors.Is(e, ErrGateUncertain) {
		t.Fatal(e)
	}
	if ticket, e = g.Capture(context.Background()); !errors.Is(e, ErrGateUncertain) || ticket != (Ticket{}) {
		t.Fatal("fresh authorization after uncertain arm", ticket, e)
	}
	if e = g.Close(context.Background()); !errors.Is(e, ErrGateUncertain) {
		t.Fatal("uncertain close hidden", e)
	}
}
func TestDriverPostReplyIdentityFailure(t *testing.T) {
	g := pipeFixture(t, "normal")
	checks := 0
	g.verify = func() error {
		checks++
		if checks > 1 {
			return errors.New("identity drift")
		}
		return nil
	}
	if e := g.Deny(context.Background()); !errors.Is(e, ErrGateUncertain) {
		t.Fatal("identity drift after reply accepted", e)
	}
}
