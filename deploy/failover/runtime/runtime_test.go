package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/deploy/failover/lease"
)

type testClock struct{ n uint64 }

func (c *testClock) Now() uint64 { return c.n }

type stepFunc func(context.Context) (lease.Result, error)

func (f stepFunc) Step(c context.Context) (lease.Result, error) { return f(c) }

type closeFunc func(context.Context) error

func (f closeFunc) Close(c context.Context) error { return f(c) }

func TestRuntimeTransitions(t *testing.T) {
	for _, scenario := range []string{"loss-with-nil-error", "partial-arm", "expired-ack", "expiry-before-renew", "step-error", "cancellation", "cleanup-error"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := Config{Interval: 10 * time.Millisecond, Duration: time.Second, Stage: StageOptions{Timeout: time.Second}}
			clock := &testClock{n: 10}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			var closed []string
			ctl := stepFunc(func(context.Context) (lease.Result, error) {
				calls++
				if calls == 1 {
					switch scenario {
					case "partial-arm":
						return lease.Result{CASAcknowledged: true}, nil
					case "expired-ack":
						clock.n = 20
					case "step-error":
						return lease.Result{}, errors.New("ambiguous CAS")
					case "cancellation":
						cancel()
					}
					return lease.Result{CASAcknowledged: true, TicketArmed: true, Ticket: lease.Ticket{Captured: 10, Deadline: 20}}, nil
				}
				return lease.Result{}, nil
			})
			if scenario == "expiry-before-renew" {
				ctl = stepFunc(func(context.Context) (lease.Result, error) {
					calls++
					clock.n = 20
					return lease.Result{CASAcknowledged: true, TicketArmed: true, Ticket: lease.Ticket{Captured: 10, Deadline: 30}}, nil
				})
			}
			gate := closeFunc(func(c context.Context) error {
				if c.Err() != nil {
					t.Error("canceled shutdown context")
				}
				closed = append(closed, "gate")
				return nil
			})
			stage := closeFunc(func(c context.Context) error {
				if c.Err() != nil {
					t.Error("canceled cleanup context")
				}
				closed = append(closed, "stage")
				if scenario == "cleanup-error" {
					return errors.New("partial cleanup")
				}
				return nil
			})
			if scenario == "expiry-before-renew" {
				// Advance only after the acknowledged ticket check, before next Step.
				reads := 0
				clock2 := clockFunc(func() uint64 {
					reads++
					if reads > 1 {
						return 30
					}
					return 20
				})
				r, e := run(ctx, cfg, ctl, clock2, stage, gate)
				if e == nil || r.State != "failed" || calls != 1 {
					t.Fatalf("expiry accepted: %+v %v calls=%d", r, e, calls)
				}
			} else {
				r, e := run(ctx, cfg, ctl, clock, stage, gate)
				if e == nil || r.State != "failed" {
					t.Fatalf("unsafe success: %+v %v", r, e)
				}
			}
			if strings.Join(closed, ",") != "gate,stage" {
				t.Fatalf("teardown order: %v", closed)
			}
		})
	}
}

type clockFunc func() uint64

func (f clockFunc) Now() uint64 { return f() }

func TestRuntimeDurationBoundsAuthorityAndPreservesCleanupError(t *testing.T) {
	cfg := Config{Interval: time.Millisecond, Duration: 30 * time.Millisecond, Stage: StageOptions{Timeout: time.Second}}
	calls := 0
	ctl := stepFunc(func(c context.Context) (lease.Result, error) { calls++; <-c.Done(); return lease.Result{}, c.Err() })
	clean := false
	begin := time.Now()
	_, err := run(context.Background(), cfg, ctl, &testClock{10}, closeFunc(func(context.Context) error { clean = true; return errors.New("cleanup diagnostic") }), closeFunc(func(context.Context) error { return errors.New("DENY uncertain") }))
	if err == nil || !strings.Contains(err.Error(), "DENY uncertain") || !strings.Contains(err.Error(), "cleanup diagnostic") || !clean || calls != 1 || time.Since(begin) > time.Second {
		t.Fatalf("unbounded or suppressed errors: %v", err)
	}
}

func TestConfigurationRefusesProofFlagsAndUnsupportedMode(t *testing.T) {
	for _, s := range []string{`{"Mode":"detached-lab-v1","Mode":"production"}`, `{"Mode":"detached-lab-v1","mode":"production"}`, `{"ClockProven":true}`, `{} {}`, `{"Driver":{"Enabled":true,"Enabled":false}}`, `{"Stage":{"Timeout":"1s"}}`} {
		var f FileConfig
		if decodeConfig([]byte(s), &f) == nil {
			t.Errorf("accepted %s", s)
		}
	}
	for _, mode := range []string{"", "production", "frontend-tc-only", "detached-lab-v1"} {
		if (Config{Mode: mode}).validate() == nil {
			t.Errorf("accepted unconfigured %s", mode)
		}
	}
}

const inventory = `[
 {"ifname":"lo","link_type":"loopback","ifindex":1,"addr_info":[{"local":"127.0.0.1"}]},
 {"ifname":"fg0","ifindex":2,"link_index":3,"linkinfo":{"info_kind":"veth"},"addr_info":[]},
 {"ifname":"fp0","ifindex":3,"link_index":2,"linkinfo":{"info_kind":"veth"},"addr_info":[]},
 {"ifname":"mg0","ifindex":4,"link_index":5,"linkinfo":{"info_kind":"veth"},"addr_info":[]},
 {"ifname":"mp0","ifindex":5,"link_index":4,"linkinfo":{"info_kind":"veth"},"addr_info":[]}]`

func TestAllPathsDetachedInventory(t *testing.T) {
	named := strings.NewReplacer(
		`"link_index":3`, `"link":"fp0"`,
		`"link_index":2`, `"link":"fg0"`,
		`"link_index":5`, `"link":"mp0"`,
		`"link_index":4`, `"link":"mg0"`,
	).Replace(inventory)
	for _, valid := range []string{inventory, named, strings.Replace(inventory, `"link_index":3`, `"link_index":3,"link":"fp0"`, 1)} {
		if e := validateInventory([]byte(valid)); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range []string{
		strings.Replace(named, `"link":"fp0"`, `"link":"other"`, 1),
		strings.Replace(named, `"link":"fp0"`, `"link":"mg0"`, 1),
		strings.Replace(named, `"link":"fp0"`, `"link":""`, 1),
		strings.Replace(inventory, `"link_index":3`, `"link_index":3,"link":"mg0"`, 1),
		strings.Replace(inventory, `"fp0"`, `"backend0"`, 1),
		strings.Replace(inventory, `"ifindex":2`, `"ifindex":2,"link_netnsid":0`, 1),
		strings.Replace(inventory, `"link_index":3`, `"link_index":5`, 1),
		strings.Replace(inventory, `"addr_info":[]`, `"addr_info":[{"local":"192.168.10.136"}]`, 1),
		strings.Replace(inventory, `"addr_info":[]`, `"addr_info":[{"local":"fe80::1"}]`, 1),
		strings.Replace(inventory, `"ifindex":4`, `"ifindex":4,"master":"br0"`, 1),
	} {
		if validateInventory([]byte(s)) == nil {
			t.Errorf("accepted alternate transmit/VIP path: %s", s)
		}
	}
}

func TestStageSubprocessFixture(t *testing.T) {
	mode := os.Getenv("FG_STAGE_FIXTURE")
	if mode == "" {
		return
	}
	request, e := line(os.Stdin)
	if e != nil {
		os.Exit(4)
	}
	nonce := strings.TrimPrefix(request, "STAGE ")
	switch mode {
	case "partial":
		os.Stdout.WriteString("STAGED ")
		os.Exit(3)
	case "stale":
		os.Stdout.WriteString("STAGED " + strings.Repeat("f", 64) + "\n")
		os.Exit(0)
	case "hang":
		time.Sleep(10 * time.Second)
		os.Exit(3)
	case "oversized":
		os.Stdout.WriteString(strings.Repeat("a", 129) + "\n")
		os.Exit(3)
	}
	os.Stdout.WriteString("STAGED " + nonce + "\n")
	request, e = line(os.Stdin)
	if e != nil || request != "CLEAN "+nonce {
		os.Exit(4)
	}
	if mode == "cleanup-stale" {
		nonce = strings.Repeat("f", 64)
	}
	os.Stdout.WriteString("CLEANED " + nonce + "\n")
	if mode == "cleanup-trailing" {
		os.Stdout.WriteString("unexpected\n")
	}
	if mode == "cleanup-exit" {
		os.Exit(7)
	}
	os.Exit(0)
}
func TestStageRealPipes(t *testing.T) {
	for _, mode := range []string{"ok", "partial", "stale", "hang", "oversized", "cleanup-stale", "cleanup-exit", "cleanup-trailing"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStageSubprocessFixture$")
			cmd.Env = append(os.Environ(), "FG_STAGE_FIXTURE="+mode, "GORACE=atexit_sleep_ms=0")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s, e := startStage(ctx, cmd, 200*time.Millisecond, strings.Repeat("a", 64))
			if mode == "ok" || strings.HasPrefix(mode, "cleanup-") {
				if e != nil {
					t.Fatal(e)
				}
				e = s.Close(ctx)
				if (mode == "ok") != (e == nil) {
					t.Fatalf("unexpected cleanup %v", e)
				}
				if s.Close(ctx) == nil {
					t.Error("terminal stage reused")
				}
			} else if e == nil {
				s.Close(ctx)
				t.Fatal("accepted ambiguous stage")
			}
		})
	}
}
func TestStageCancellation(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestStageSubprocessFixture$")
	cmd.Env = append(os.Environ(), "FG_STAGE_FIXTURE=hang", "GORACE=atexit_sleep_ms=0")
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	begin := time.Now()
	s, e := startStage(ctx, cmd, time.Second, strings.Repeat("a", 64))
	if e == nil {
		s.Close(context.Background())
		t.Fatal("cancel accepted")
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatal("cancel unbounded")
	}
}

func TestRuntimeNormalBoundedCompletion(t *testing.T) {
	// Give the step both timers' ready state. The next loop must not call an
	// authority with an expired end context even if it selects the ticker first.
	for range 20 {
		cfg := Config{Interval: time.Millisecond, Duration: 3 * time.Millisecond, Stage: StageOptions{Timeout: time.Second}}
		calls := 0
		ctl := stepFunc(func(ctx context.Context) (lease.Result, error) {
			calls++
			if ctx.Err() != nil {
				t.Error("step called after total run deadline")
			}
			return lease.Result{CASAcknowledged: true, TicketArmed: true, Ticket: lease.Ticket{Captured: 1, Deadline: 100}}, nil
		})
		close := closeFunc(func(context.Context) error { return nil })
		r, e := run(context.Background(), cfg, ctl, clockFunc(func() uint64 { time.Sleep(5 * time.Millisecond); return 10 }), close, close)
		if e != nil || r.State != "lab-stopped" || r.Arms != 1 || calls != 1 {
			t.Fatalf("invalid completion %+v %v calls=%d", r, e, calls)
		}
	}
}

func TestStageCloseLockHonorsCancellation(t *testing.T) {
	s := &stageProcess{serial: make(chan struct{}, 1)}
	s.serial <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if e := s.Close(ctx); !errors.Is(e, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("unbounded Close lock: %v", e)
	}
}
