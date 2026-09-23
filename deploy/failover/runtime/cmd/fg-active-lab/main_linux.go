//go:build linux

// Parent-only lab owner. No production mode and no raw packet capability.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/piwi3910/nexora/deploy/failover/lease"
	lab "github.com/piwi3910/nexora/deploy/failover/runtime"
	"golang.org/x/sys/unix"
)

type delayed struct{ lease.Authority }

func (d delayed) CAS(ctx context.Context, old, next lease.Record) (lease.Record, error) {
	r, e := d.Authority.CAS(ctx, old, next)
	// Deliberately withhold an ACTUAL acknowledgement beyond the captured window.
	// This is a negative fixture, not an authority retry or refreshed grant.
	if e == nil {
		time.Sleep(6 * time.Second)
	}
	return r, e
}

func execute() (err error) {
	config := flag.String("config", "", "explicit private lab config")
	manifest := flag.String("manifest", "", "exact lab manifest")
	scenario := flag.String("scenario", "", "expiry, stop, death, delayed, partial")
	enabled := flag.Bool("execute-isolated-active-lab", false, "parent-only opt-in")
	flag.Parse()
	if !*enabled || flag.NArg() != 0 || os.Getuid() != 0 || os.Geteuid() != 0 {
		return errors.New("explicit isolated root lab required; privilege separation unsupported")
	}
	switch *scenario {
	case "expiry", "stop", "death", "delayed", "partial":
	default:
		return errors.New("unknown scenario")
	}
	// setpriv must remove CAP_NET_RAW before Go starts, across ALL threads.
	// A per-thread capset inside a multithreaded owner is insufficient.
	tasks, e := os.ReadDir("/proc/self/task")
	if e != nil {
		return e
	}
	for _, task := range tasks {
		status, e := os.ReadFile("/proc/self/task/" + task.Name() + "/status")
		if e != nil {
			return e
		}
		seen := 0
		for _, line := range strings.Split(string(status), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			switch fields[0] {
			case "CapEff:", "CapPrm:", "CapInh:", "CapBnd:", "CapAmb:":
				n, e := strconv.ParseUint(fields[1], 16, 64)
				if e != nil || n&(1<<unix.CAP_NET_RAW) != 0 {
					return errors.New("owner inherited CAP_NET_RAW")
				}
				seen++
			}
		}
		if seen != 5 {
			return errors.New("unknown owner capability inventory")
		}
	}
	cfg, err := lab.LoadActiveLabConfig(*config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// Inspect DOWN inventory before the privileged driver sees any assignment.
	phase := func(action string) error {
		script := filepath.Join(filepath.Dir(cfg.Stage.Script), "active_probe.py")
		cmd := exec.CommandContext(ctx, cfg.Stage.Python, "-I", "-B", script, "--phase", action, "--manifest", *manifest)
		cmd.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin"}
		cmd.WaitDelay = time.Second
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	if err = lab.ValidateActiveCode(cfg, *manifest); err != nil {
		return err
	}
	if err = phase("down"); err != nil {
		return err
	}
	gate, clock, err := lease.LaunchDriver(ctx, cfg.Driver)
	if err != nil {
		return err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, gate.Close(stop))
	}()
	if err = gate.Deny(ctx); err != nil {
		return err
	}
	if *scenario == "partial" {
		e := phase("partial")
		var exit *exec.ExitError
		if !errors.As(e, &exit) || exit.ExitCode() != 42 {
			return errors.Join(e, errors.New("partial injection not reached"))
		}
		fmt.Println("PARTIAL-REFUSED")
		return errors.New("injected partial activation; no CAS attempted")
	}
	if err = phase("activate"); err != nil {
		return err
	}
	authority, err := lease.NewHTTPS(cfg.Authority)
	if err != nil {
		return err
	}
	var auth lease.Authority = authority
	if *scenario == "delayed" {
		auth = delayed{authority}
	}
	holder, err := lease.NewHolderID()
	if err != nil {
		return err
	}
	ctl, err := lease.New(auth, gate, clock, lease.Config{Holder: holder, Margin: cfg.Margin, IOTimeout: cfg.Driver.IOTimeout})
	if err != nil {
		return err
	}
	r, err := ctl.Step(ctx)
	if err != nil || r.TicketArmed {
		return errors.Join(err, errors.New("initial quarantine required"))
	}
	fmt.Println("QUARANTINED")
	time.Sleep(lease.MaxKernelWindow + cfg.Margin)
	r, err = ctl.Step(ctx)
	if *scenario == "delayed" {
		if err == nil || r.TicketArmed {
			return errors.New("delayed grant was accepted")
		}
		fmt.Println("DELAYED-REFUSED")
		return nil
	}
	if err != nil {
		return err
	}
	if !r.TicketArmed || !r.CASAcknowledged {
		return errors.New("incomplete grant")
	}
	fmt.Printf("ARMED %d %d\n", r.Ticket.Captured, r.Ticket.Deadline)
	// No renewal: parent independently probes expiry, SIGSTOP and process death.
	time.Sleep(12 * time.Second)
	return nil
}
func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
