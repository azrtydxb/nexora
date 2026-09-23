// Package lease is an unactivated Kubernetes CAS ownership candidate.
package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const MaxKernelWindow = 5 * time.Second
const Protocol = "nexora-kernel-ticket-v1"

// Clock must return this boot's zero-offset CLOCK_BOOTTIME nanoseconds, in the
// same domain as Gate. No wall-clock or portable time.Now fallback is provided.
type Clock interface{ Now() uint64 }
type Ticket struct{ Captured, Deadline uint64 }

// Gate is the integration boundary to fence/driver.c: Capture sends
// CAPTURE and parses TICKET; Arm sends ARM <Captured> for the SAME outstanding
// ticket and requires ARMED; Deny sends DENY and requires DENIED. Implementations
// must bound I/O, validate process/boot/namespace identity, and never recapture
// inside Arm. Errors may mean the operation took effect.
type Gate interface {
	Capture(context.Context) (Ticket, error)
	Arm(context.Context, Ticket) error
	Deny(context.Context) error
}

type Record struct {
	UID, RV, Holder, Nonce, Protocol string
	Epoch                            uint64
	raw                              map[string]any
}
type Authority interface {
	Get(context.Context) (Record, error)
	// CAS must use old's exact UID/RV; no retries, creates, deletes or redirects.
	CAS(context.Context, Record, Record) (Record, error)
}

// NewHolderID creates a strong per-process-incarnation identity. Never persist
// and reuse it, or derive it solely from a management instance ID.
func NewHolderID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func strong(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func valid(r Record) bool {
	return r.Protocol == Protocol && r.UID != "" && len(r.UID) <= 256 && r.RV != "" && len(r.RV) <= 256 &&
		(r.Holder == "" || strong(r.Holder)) && strong(r.Nonce)
}

type Config struct {
	Holder string
	// Margin must include clock-rate error and bounded post-gate drain. A
	// positive value alone does not establish those platform assumptions.
	Margin    time.Duration
	IOTimeout time.Duration
}
type Result struct {
	// Historical acknowledgements only, never dataplane eligibility/health.
	CASAcknowledged, TicketArmed bool
	Ticket                       Ticket
}
type Controller struct {
	mu                     sync.Mutex
	a                      Authority
	g                      Gate
	c                      Clock
	cfg                    Config
	last, since            uint64
	pinned, stableRV       string
	own                    Record
	observed               Record
	ack, started, terminal bool
}

func New(a Authority, g Gate, c Clock, cfg Config) (*Controller, error) {
	if a == nil || g == nil || c == nil || !strong(cfg.Holder) || cfg.Margin <= 0 || cfg.Margin > time.Duration(math.MaxInt64)-MaxKernelWindow || cfg.IOTimeout <= 0 || cfg.IOTimeout > time.Minute {
		return nil, errors.New("invalid controller configuration")
	}
	if driver, ok := g.(*DriverGate); ok {
		paired, ok := c.(*BootClock)
		if driver == nil || !ok || paired == nil || driver.clock != paired || driver.poisoned.Load() || !driver.claimed.CompareAndSwap(false, true) {
			return nil, errors.New("driver must be paired with its clock and bound to one controller")
		}
	}
	return &Controller{a: a, g: g, c: c, cfg: cfg}, nil
}
func (c *Controller) now() (uint64, error) {
	n := c.c.Now()
	if n == 0 || n < c.last {
		return 0, errors.New("invalid or regressing boottime")
	}
	c.last = n
	return n, nil
}
func (c *Controller) deny() error {
	// An expired/canceled authority context must not suppress the DENY attempt.
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.IOTimeout)
	defer cancel()
	if err := c.g.Deny(ctx); err != nil || ctx.Err() != nil {
		c.terminal = true
		return errors.New("gate DENY failed: enforcement uncertain; controller terminal")
	}
	return nil
}
func (c *Controller) fail(err error, terminal bool) (Result, error) {
	c.ack = false
	c.stableRV = ""
	c.since = 0
	c.terminal = c.terminal || terminal
	return Result{}, errors.Join(err, c.deny())
}

// Step performs at most one GET and one PUT. Callers schedule observations; no
// failure is hidden by retries. Every failure discards cached authorization.
func (c *Controller) Step(ctx context.Context) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminal {
		return Result{}, errors.New("controller terminal; enforcement may be uncertain")
	}
	if !c.started {
		c.started = true
		if err := c.deny(); err != nil {
			return Result{}, err
		}
	}
	if _, err := c.now(); err != nil {
		return c.fail(err, true)
	}
	op, cancel := context.WithTimeout(ctx, c.cfg.IOTimeout)
	defer cancel()
	r, err := c.a.Get(op)
	if err != nil {
		return c.fail(errors.New("authority GET failed"), errors.Is(err, ErrGone))
	}
	if op.Err() != nil {
		return c.fail(errors.New("authority GET deadline exceeded"), false)
	}
	n, err := c.now()
	if err != nil {
		return c.fail(err, true)
	}
	if !valid(r) {
		return c.fail(errors.New("invalid lease protocol or record"), false)
	}
	if c.pinned == "" {
		c.pinned = r.UID
	}
	if r.UID != c.pinned {
		return c.fail(errors.New("lease UID replaced; reprovisioning required"), true)
	}
	if c.observed.UID == r.UID && c.observed.RV == r.RV &&
		(c.observed.Holder != r.Holder || c.observed.Nonce != r.Nonce || c.observed.Epoch != r.Epoch) {
		return c.fail(errors.New("record changed without resourceVersion mutation"), false)
	}
	c.observed = r
	renew := c.ack && r.UID == c.own.UID && r.RV == c.own.RV && r.Holder == c.cfg.Holder && r.Nonce == c.own.Nonce && r.Epoch == c.own.Epoch
	if !renew {
		c.ack = false
		if c.stableRV != r.RV {
			c.stableRV = r.RV
			c.since = n
			if err := c.deny(); err != nil {
				return Result{}, err
			}
			return Result{}, nil
		}
		if n-c.since < uint64(MaxKernelWindow+c.cfg.Margin) {
			return Result{}, nil
		}
	}
	if r.Epoch == math.MaxUint64 {
		return c.fail(errors.New("epoch overflow"), false)
	}
	nonce, err := NewHolderID()
	if err != nil {
		return c.fail(errors.New("nonce generation failed"), false)
	}
	if nonce == r.Nonce {
		return c.fail(errors.New("nonce collision"), false)
	}
	before, err := c.now()
	if err != nil {
		return c.fail(err, true)
	}
	t, err := c.g.Capture(op)
	if err != nil {
		return c.fail(errors.New("gate CAPTURE failed; enforcement uncertain"), true)
	}
	if op.Err() != nil {
		return c.fail(errors.New("gate CAPTURE acknowledgement deadline exceeded; enforcement uncertain"), true)
	}
	after, err := c.now()
	if err != nil {
		return c.fail(err, true)
	}
	if t.Captured == 0 || t.Captured < before || t.Captured > after || t.Deadline <= t.Captured || t.Deadline-t.Captured > uint64(MaxKernelWindow) || after >= t.Deadline {
		return c.fail(errors.New("invalid or expired kernel ticket"), true)
	}
	next := r
	next.Holder = c.cfg.Holder
	next.Nonce = nonce
	next.Epoch++
	out, err := c.a.CAS(op, r, next)
	if err != nil {
		return c.fail(errors.New("authority CAS failed or ambiguous"), errors.Is(err, ErrGone))
	}
	n, err = c.now()
	if err != nil {
		return c.fail(err, true)
	}
	if !valid(out) || out.UID != r.UID || out.RV == r.RV || out.Holder != next.Holder || out.Nonce != next.Nonce || out.Epoch != next.Epoch {
		return c.fail(errors.New("unverified CAS acknowledgement"), false)
	}
	if n >= t.Deadline {
		return c.fail(errors.New("CAS acknowledged after ticket expiry"), false)
	}
	if err := op.Err(); err != nil {
		return c.fail(errors.New("CAS operation deadline exceeded"), false)
	}
	if err := c.g.Arm(op, t); err != nil {
		return c.fail(errors.New("gate ARM failed; enforcement uncertain"), true)
	}
	if op.Err() != nil {
		return c.fail(errors.New("gate ARM acknowledgement deadline exceeded; enforcement uncertain"), true)
	}
	n, err = c.now()
	if err != nil {
		return c.fail(err, true)
	}
	if n >= t.Deadline {
		return c.fail(errors.New("ARM acknowledgement after ticket expiry"), false)
	}
	c.own = out
	c.ack = true
	c.stableRV = ""
	c.since = 0
	return Result{CASAcknowledged: true, TicketArmed: true, Ticket: t}, nil
}

func (r Record) String() string { return fmt.Sprintf("Lease record epoch=%d", r.Epoch) }
