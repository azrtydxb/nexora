package lease

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DriverOptions is an explicit, privileged lab opt-in. Paths and their parents
// must be immutable root-owned code. UID is the caller identity, not code trust.
// Privileged-helper/unprivileged-owner separation is unsupported.
// No namespace entry, credentials, inherited environment, or frontend activation
// is performed. Namespace ownership/exclusive administration is an external fact.
type DriverOptions struct {
	// Nonempty pair selects only the isolated dual-egress lab driver protocol.
	LabBackendInterface, LabBackendAlias     string
	Enabled                                  bool
	DriverPath, ObjectPath, Interface, Alias string
	BootID, NetworkNamespace, TimeNamespace  string
	UID                                      uint32
	IOTimeout, ReapTimeout                   time.Duration
}

var ErrGateUncertain = errors.New("driver operation uncertain; connection terminal; kernel expiry still required")

// DriverGate owns exactly one child and its private pipes. Do not use methods
// directly after handing it to New. Close never removes kernel attachments.
type DriverGate struct {
	input, output    *os.File
	process          *os.Process
	done             chan struct{}
	serial           chan struct{}
	timeout, reap    time.Duration
	poisonOnce       sync.Once
	poisoned         atomic.Bool
	claimed          atomic.Bool
	clock            *BootClock
	verify           func() error
	ticket           Ticket
	lastCapture      uint64
	ifindex, program uint64
	backend          uint64
	dual             bool
}

func decimal(s string, bits int) (uint64, error) {
	if s == "" || len(s) > 20 || (len(s) > 1 && s[0] == '0') {
		return 0, ErrGateUncertain
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, ErrGateUncertain
		}
	}
	n, e := strconv.ParseUint(s, 10, bits)
	if e != nil || n == 0 {
		return 0, ErrGateUncertain
	}
	return n, nil
}

// One byte per read deliberately avoids buffering a second acknowledgement.
// There is no recovery/resynchronization in this unsequenced trusted protocol.
func readDriverLine(f *os.File) (string, error) {
	b := make([]byte, 0, 127)
	var one [1]byte
	for len(b) < 127 {
		n, e := f.Read(one[:])
		if e != nil {
			return "", e
		}
		if n != 1 {
			return "", io.ErrNoProgress
		}
		if one[0] == '\n' {
			return string(b), nil
		}
		if one[0] < 32 || one[0] > 126 {
			return "", ErrGateUncertain
		}
		b = append(b, one[0])
	}
	return "", ErrGateUncertain
}

func (g *DriverGate) poison() {
	g.poisonOnce.Do(func() { g.poisoned.Store(true); _ = g.input.Close(); _ = g.output.Close(); _ = g.process.Kill() })
}
func (g *DriverGate) reaped() error {
	timer := time.NewTimer(g.reap)
	defer timer.Stop()
	select {
	case <-g.done:
		return nil
	case <-timer.C:
		return errors.New("driver reap deadline exceeded; enforcement uncertain")
	}
}

// exchange bounds lock acquisition and actual pollable pipe I/O, including
// cancellation without a deadline. Caller validation runs under the same lock.
func (g *DriverGate) exchange(ctx context.Context, command string, accept func(string) error) error {
	return g.exchangeChecked(ctx, command, nil, accept)
}
func (g *DriverGate) exchangeChecked(ctx context.Context, command string, before func() error, accept func(string) error) error {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	select {
	case g.serial <- struct{}{}:
	case <-ctx.Done():
		g.poison()
		return ErrGateUncertain
	}
	defer func() { <-g.serial }()
	if g.poisoned.Load() {
		return ErrGateUncertain
	}
	stop := context.AfterFunc(ctx, g.poison)
	defer stop()
	deadline, _ := ctx.Deadline()
	if g.input.SetWriteDeadline(deadline) != nil || g.output.SetReadDeadline(deadline) != nil {
		g.poison()
		return ErrGateUncertain
	}
	var err error
	if g.verify != nil {
		err = g.verify()
	}
	if err == nil && before != nil {
		err = before()
	}
	if err != nil {
		g.poison()
		return ErrGateUncertain
	}
	if command != "" {
		var n int
		n, err = io.WriteString(g.input, command+"\n")
		if err == nil && n != len(command)+1 {
			err = io.ErrShortWrite
		}
	}
	var line string
	if err == nil {
		line, err = readDriverLine(g.output)
	}
	if err == nil {
		err = accept(line)
	}
	if err == nil && g.verify != nil {
		err = g.verify()
	}
	if !stop() || err != nil || ctx.Err() != nil || g.poisoned.Load() {
		g.poison()
		return ErrGateUncertain
	}
	return nil
}
func (g *DriverGate) ready(ctx context.Context) error {
	return g.exchange(ctx, "", func(line string) error {
		p := strings.Split(line, " ")
		if g.dual {
			if len(p) != 5 || p[0] != "READY2" || !strings.HasPrefix(p[2], "backend=") {
				return ErrGateUncertain
			}
			var err error
			g.backend, err = decimal(strings.TrimPrefix(p[2], "backend="), 31)
			if err != nil {
				return err
			}
			p = []string{"READY", p[1], p[3], p[4]}
		}
		if len(p) != 4 || p[0] != "READY" || !strings.HasPrefix(p[1], "ifindex=") || !strings.HasPrefix(p[2], "program=") || p[3] != "horizon_ns=5000000000" {
			return ErrGateUncertain
		}
		var e error
		g.ifindex, e = decimal(strings.TrimPrefix(p[1], "ifindex="), 31)
		if e != nil || (g.dual && g.backend == g.ifindex) {
			return ErrGateUncertain
		}
		g.program, e = decimal(strings.TrimPrefix(p[2], "program="), 32)
		return e
	})
}
func (g *DriverGate) Capture(ctx context.Context) (Ticket, error) {
	var result Ticket
	err := g.exchangeChecked(ctx, "CAPTURE", func() error {
		if g.ticket != (Ticket{}) {
			return ErrGateUncertain
		}
		return nil
	}, func(line string) error {
		p := strings.Split(line, " ")
		if len(p) != 3 || p[0] != "TICKET" {
			return ErrGateUncertain
		}
		c, e := decimal(p[1], 64)
		if e != nil {
			return e
		}
		d, e := decimal(p[2], 64)
		if e != nil {
			return e
		}
		if c <= g.lastCapture || d <= c || d-c != uint64(MaxKernelWindow) {
			return ErrGateUncertain
		}
		result = Ticket{c, d}
		g.ticket = result
		g.lastCapture = c
		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return result, nil
}
func (g *DriverGate) Arm(ctx context.Context, t Ticket) error {
	return g.exchangeChecked(ctx, fmt.Sprintf("ARM %d", t.Captured), func() error {
		if t.Captured == 0 || t != g.ticket {
			return ErrGateUncertain
		}
		return nil
	}, func(line string) error {
		if line != "ARMED" {
			return ErrGateUncertain
		}
		g.ticket = Ticket{}
		return nil
	})
}
func (g *DriverGate) Deny(ctx context.Context) error {
	return g.exchange(ctx, "DENY", func(line string) error {
		if line != "DENIED" {
			return ErrGateUncertain
		}
		g.ticket = Ticket{}
		return nil
	})
}

// Close attempts a bounded DENY then permanently closes/kills/reaps the child.
// A failure is never evidence that forwarding is inactive. Repeated Close on a
// terminal connection continues to report uncertainty.
func (g *DriverGate) Close(ctx context.Context) error {
	err := g.Deny(ctx)
	g.poison()
	return errors.Join(err, g.reaped())
}

// startPrivate is deliberately unexported: production entry is LaunchDriver,
// which checks Linux identity/trusted paths before executing an absolute binary.
func startPrivate(ctx context.Context, cmd *exec.Cmd, timeout, reap time.Duration, dual ...bool) (*DriverGate, error) {
	inR, inW, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	outR, outW, e := os.Pipe()
	if e != nil {
		inR.Close()
		inW.Close()
		return nil, e
	}
	cleanup := func() { inR.Close(); inW.Close(); outR.Close(); outW.Close() }
	// os.Pipe must use the runtime poller. Never fall back to a goroutine whose
	// blocking read/write cannot be interrupted by deadline/Close.
	if inW.SetWriteDeadline(time.Now().Add(timeout)) != nil || outR.SetReadDeadline(time.Now().Add(timeout)) != nil {
		cleanup()
		return nil, errors.New("private pipes lack deadlines")
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = nil
	if e = cmd.Start(); e != nil {
		cleanup()
		return nil, errors.New("driver launch failed")
	}
	inR.Close()
	outW.Close()
	g := &DriverGate{input: inW, output: outR, process: cmd.Process, done: make(chan struct{}), serial: make(chan struct{}, 1), timeout: timeout, reap: reap}
	go func() { _ = cmd.Wait(); close(g.done) }()
	if len(dual) == 1 {
		g.dual = dual[0]
	}
	if e = g.ready(ctx); e != nil {
		g.poison()
		return nil, errors.Join(e, g.reaped())
	}
	return g, nil
}
