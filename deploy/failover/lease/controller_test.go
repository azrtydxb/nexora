package lease

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()
var boom = errors.New("injected")

type fakeClock struct{ n uint64 }

func (c *fakeClock) Now() uint64         { return c.n }
func (c *fakeClock) add(d time.Duration) { c.n += uint64(d) }

type fakeGate struct {
	c                           *fakeClock
	ticket                      Ticket
	armed                       []Ticket
	denies, captures            int
	captureErr, armErr, denyErr error
	change                      func(*Ticket)
	armDelay                    time.Duration
	armHook                     func()
}

func (g *fakeGate) Capture(context.Context) (Ticket, error) {
	g.captures++
	g.ticket = Ticket{g.c.n, g.c.n + uint64(MaxKernelWindow)}
	if g.change != nil {
		g.change(&g.ticket)
	}
	return g.ticket, g.captureErr
}
func (g *fakeGate) Arm(_ context.Context, t Ticket) error {
	if t != g.ticket {
		return errors.New("different ticket")
	}
	g.armed = append(g.armed, t)
	if g.armHook != nil {
		g.armHook()
	}
	g.c.add(g.armDelay)
	return g.armErr
}
func (g *fakeGate) Deny(context.Context) error { g.denies++; return g.denyErr }

type fakeAuthority struct {
	r              Record
	getErr, casErr error
	stale          *Record
	puts           int
	commitOnError  bool
	before         func()
	output         func(*Record)
}

func (a *fakeAuthority) Get(context.Context) (Record, error) {
	if a.stale != nil {
		return *a.stale, a.getErr
	}
	return a.r, a.getErr
}
func (a *fakeAuthority) CAS(_ context.Context, old, next Record) (Record, error) {
	a.puts++
	if a.before != nil {
		a.before()
	}
	if old.UID != a.r.UID || old.RV != a.r.RV {
		return Record{}, boom
	}
	if a.casErr != nil && !a.commitOnError {
		return Record{}, a.casErr
	}
	next.RV = fmt.Sprintf("%s-next-%d", old.RV, a.puts)
	a.r = next
	out := next
	if a.output != nil {
		a.output(&out)
	}
	return out, a.casErr
}
func fixture(t *testing.T) (*Controller, *fakeAuthority, *fakeGate, *fakeClock) {
	t.Helper()
	clock := &fakeClock{n: 1}
	a := &fakeAuthority{r: Record{UID: "uid", RV: "1", Nonce: strings.Repeat("a", 64), Protocol: Protocol}}
	g := &fakeGate{c: clock}
	c, err := New(a, g, clock, Config{Holder: strings.Repeat("b", 64), Margin: time.Second, IOTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c, a, g, clock
}
func stepOK(t *testing.T, c *Controller) Result {
	t.Helper()
	r, e := c.Step(ctx)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func acquire(t *testing.T, c *Controller, clock *fakeClock) {
	t.Helper()
	if stepOK(t, c).TicketArmed {
		t.Fatal("startup armed")
	}
	clock.add(6 * time.Second)
	if !stepOK(t, c).TicketArmed {
		t.Fatal("no grant")
	}
}
func failed(t *testing.T, c *Controller, g *fakeGate) {
	t.Helper()
	arms, denies := len(g.armed), g.denies
	r, e := c.Step(ctx)
	if e == nil || r.TicketArmed || r.CASAcknowledged || len(g.armed) != arms || g.denies <= denies {
		t.Fatalf("unsafe failure r=%+v err=%v arms=%d denies=%d", r, e, len(g.armed), g.denies)
	}
}

func TestStartupQuarantineAndRenewal(t *testing.T) {
	c, a, g, clock := fixture(t)
	stepOK(t, c)
	clock.add(6*time.Second - time.Nanosecond)
	if stepOK(t, c).TicketArmed {
		t.Fatal("early")
	}
	clock.add(time.Nanosecond)
	if !stepOK(t, c).TicketArmed {
		t.Fatal("not granted")
	}
	old := a.r
	clock.add(time.Second)
	if !stepOK(t, c).TicketArmed || a.r.Nonce == old.Nonce || a.r.RV == old.RV || a.r.Epoch != old.Epoch+1 {
		t.Fatal("renewal did not mutate")
	}
	if len(g.armed) != 2 {
		t.Fatal("arms")
	}
}
func TestTwoContendersAndStaleOwner(t *testing.T) {
	aOwner, a, ga, clock := fixture(t)
	gb := &fakeGate{c: clock}
	b, _ := New(a, gb, clock, Config{Holder: strings.Repeat("c", 64), Margin: time.Second, IOTimeout: time.Second})
	stepOK(t, aOwner)
	stepOK(t, b)
	clock.add(6 * time.Second)
	stepOK(t, aOwner)
	if stepOK(t, b).TicketArmed {
		t.Fatal("did not reset after renewal")
	}
	clock.add(5 * time.Second)
	if stepOK(t, b).TicketArmed {
		t.Fatal("early takeover")
	}
	clock.add(time.Second)
	stale := a.r
	if !stepOK(t, b).TicketArmed {
		t.Fatal("no takeover")
	}
	a.stale = &stale
	failed(t, aOwner, ga)
	a.stale = nil
	if stepOK(t, aOwner).TicketArmed {
		t.Fatal("stale owner reused authority")
	}
}
func TestRenewalResetsQuarantine(t *testing.T) {
	c, a, _, clock := fixture(t)
	stepOK(t, c)
	clock.add(5 * time.Second)
	a.r.RV = "2"
	stepOK(t, c)
	clock.add(time.Second)
	if stepOK(t, c).TicketArmed {
		t.Fatal("old interval survived")
	}
	clock.add(5 * time.Second)
	if !stepOK(t, c).TicketArmed {
		t.Fatal("no grant")
	}
}
func TestStaleCachedReadsCASConflict(t *testing.T) {
	c, a, g, clock := fixture(t)
	old := a.r
	a.stale = &old
	stepOK(t, c)
	a.r.RV = "new"
	clock.add(6 * time.Second)
	failed(t, c, g)
	if a.puts != 1 {
		t.Fatal("retry")
	}
}
func TestPartitionCancelsIntervalAndOwnership(t *testing.T) {
	c, a, g, clock := fixture(t)
	acquire(t, c, clock)
	a.getErr = boom
	failed(t, c, g)
	a.getErr = nil
	clock.add(10 * time.Second)
	if stepOK(t, c).TicketArmed {
		t.Fatal("cached ownership")
	}
	clock.add(6 * time.Second)
	if !stepOK(t, c).TicketArmed {
		t.Fatal("not revalidated")
	}
}
func TestDelayedAndAmbiguousGrant(t *testing.T) {
	for _, mode := range []string{"late", "ambiguous", "conflict", "unchanged-rv", "wrong-nonce", "wrong-holder", "wrong-uid", "wrong-epoch", "bad-protocol"} {
		t.Run(mode, func(t *testing.T) {
			c, a, g, clock := fixture(t)
			stepOK(t, c)
			clock.add(6 * time.Second)
			switch mode {
			case "late":
				a.before = func() { clock.add(5 * time.Second) }
			case "ambiguous":
				a.casErr = boom
				a.commitOnError = true
			case "conflict":
				a.casErr = boom
			default:
				a.output = func(r *Record) {
					switch mode {
					case "unchanged-rv":
						r.RV = "1"
					case "wrong-nonce":
						r.Nonce = strings.Repeat("a", 64)
					case "wrong-holder":
						r.Holder = ""
					case "wrong-uid":
						r.UID = "other"
					case "wrong-epoch":
						r.Epoch++
					case "bad-protocol":
						r.Protocol = "other"
					}
				}
			}
			failed(t, c, g)
			if g.captures != 1 || a.puts != 1 {
				t.Fatal("operation retried")
			}
			a.before = nil
			a.output = nil
			a.casErr = nil
			if stepOK(t, c).TicketArmed {
				t.Fatal("bad output reused")
			}
		})
	}
}
func TestDeletionReplacementAndProtocol(t *testing.T) {
	for _, mode := range []string{"deleted", "replaced", "protocol", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			c, a, g, clock := fixture(t)
			stepOK(t, c)
			clock.add(6 * time.Second)
			switch mode {
			case "deleted":
				a.getErr = ErrGone
			case "replaced":
				a.r.UID = "new"
			case "protocol":
				a.r.Protocol = "other"
			case "overflow":
				a.r.Epoch = math.MaxUint64
			}
			failed(t, c, g)
			if a.puts != 0 {
				t.Fatal("write")
			}
		})
	}
}
func TestIncarnationAlwaysQuarantines(t *testing.T) {
	c, a, _, clock := fixture(t)
	acquire(t, c, clock)
	g := &fakeGate{c: clock}
	h, e := NewHolderID()
	if e != nil {
		t.Fatal(e)
	}
	fresh, _ := New(a, g, clock, Config{Holder: h, Margin: time.Second, IOTimeout: time.Second})
	if stepOK(t, fresh).TicketArmed {
		t.Fatal("management identity bypass")
	}
	// Even an accidentally reused holder cannot bypass startup quarantine.
	fresh, _ = New(a, g, clock, c.cfg)
	if stepOK(t, fresh).TicketArmed {
		t.Fatal("restart bypass")
	}
}
func TestTicketBoundsAndClock(t *testing.T) {
	for _, mode := range []string{"zero", "future", "old", "reversed", "long", "expired", "regress", "capture-error"} {
		t.Run(mode, func(t *testing.T) {
			c, a, g, clock := fixture(t)
			stepOK(t, c)
			clock.add(6 * time.Second)
			g.change = func(x *Ticket) {
				switch mode {
				case "zero":
					x.Captured = 0
				case "future":
					x.Captured++
				case "old":
					x.Captured--
				case "reversed":
					x.Deadline = x.Captured
				case "long":
					x.Deadline++
				case "expired":
					clock.add(5 * time.Second)
				case "regress":
					clock.n = 1
				}
			}
			if mode == "capture-error" {
				g.captureErr = boom
			}
			failed(t, c, g)
			if a.puts != 0 {
				t.Fatal("bad ticket reached CAS")
			}
			if !c.terminal {
				t.Fatal("gate/clock not terminal")
			}
		})
	}
}
func TestPausedDriverAndGateFailure(t *testing.T) {
	for _, mode := range []string{"arm-error", "deny-error", "paused-arm"} {
		t.Run(mode, func(t *testing.T) {
			c, _, g, clock := fixture(t)
			stepOK(t, c)
			clock.add(6 * time.Second)
			switch mode {
			case "arm-error":
				g.armErr = boom
			case "deny-error":
				g.denyErr = boom
				clock.n = 0
			case "paused-arm":
				g.armDelay = 5 * time.Second
			}
			r, e := c.Step(ctx)
			if e == nil || r.TicketArmed {
				t.Fatal("unsafe acknowledgement")
			}
			if mode != "paused-arm" && !c.terminal {
				t.Fatal("not terminal")
			}
			if mode == "deny-error" && !strings.Contains(e.Error(), "uncertain") {
				t.Fatal("missing uncertainty")
			}
		})
	}
}
func TestConcurrentStepsSerialized(t *testing.T) {
	c, _, _, _ := fixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Step(ctx) }()
	}
	wg.Wait()
}
func TestInvalidConfig(t *testing.T) {
	c, a, g, clock := fixture(t)
	for _, cfg := range []Config{{}, {Holder: c.cfg.Holder, Margin: -1, IOTimeout: time.Second}, {Holder: c.cfg.Holder, Margin: time.Duration(math.MaxInt64), IOTimeout: time.Second}} {
		if _, e := New(a, g, clock, cfg); e == nil {
			t.Fatal("accepted invalid config")
		}
	}
}

func TestUnchangedVersionMutation(t *testing.T) {
	c, a, g, clock := fixture(t)
	stepOK(t, c)
	clock.add(6 * time.Second)
	a.r.Nonce = strings.Repeat("d", 64)
	failed(t, c, g)
	if a.puts != 0 {
		t.Fatal("mutated version accepted")
	}
}
func TestCanceledAcknowledgements(t *testing.T) {
	for _, phase := range []string{"get", "capture", "arm"} {
		t.Run(phase, func(t *testing.T) {
			c, a, g, clock := fixture(t)
			stepOK(t, c)
			clock.add(6 * time.Second)
			op, cancel := context.WithCancel(ctx)
			defer cancel()
			switch phase {
			case "get":
				cancel()
			case "capture":
				g.change = func(*Ticket) { cancel() }
			case "arm":
				g.armHook = cancel
			}
			denies := g.denies
			r, e := c.Step(op)
			if e == nil || r.TicketArmed || g.denies <= denies {
				t.Fatalf("canceled ack accepted: %+v %v", r, e)
			}
			if phase != "arm" && (len(g.armed) != 0 || a.puts != 0) {
				t.Fatal("operation continued after cancellation")
			}
			if phase != "get" && !c.terminal {
				t.Fatal("gate bad acknowledgement not terminal")
			}
		})
	}
}
func TestReappearanceCannotReviveDeletedController(t *testing.T) {
	c, a, g, clock := fixture(t)
	stepOK(t, c)
	a.getErr = ErrGone
	failed(t, c, g)
	a.getErr = nil
	a.r.UID = "replacement"
	clock.add(20 * time.Second)
	if r, e := c.Step(ctx); e == nil || r.TicketArmed || a.puts != 0 {
		t.Fatal("deleted controller revived")
	}
	fresh, e := New(a, g, clock, c.cfg)
	if e != nil {
		t.Fatal(e)
	}
	if stepOK(t, fresh).TicketArmed {
		t.Fatal("recreated object bypassed startup quarantine")
	}
}
