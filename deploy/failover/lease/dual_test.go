package lease

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestDualReadyCannotDowngradeOrCollapseHooks(t *testing.T) {
	for _, tc := range []struct {
		line     string
		dual, ok bool
	}{
		{"READY2 ifindex=2 backend=4 program=3 horizon_ns=5000000000", true, true},
		{"READY ifindex=2 program=3 horizon_ns=5000000000", true, false},
		{"READY2 ifindex=2 backend=4 program=3 horizon_ns=5000000000", false, false},
		{"READY2 ifindex=2 backend=2 program=3 horizon_ns=5000000000", true, false},
		{"READY2 ifindex=2 backend=04 program=3 horizon_ns=5000000000", true, false},
		{"READY2 ifindex=2 backend=0 program=3 horizon_ns=5000000000", true, false},
		{"READY2 ifindex=2 backend=4 program=3 horizon_ns=5000000000 extra", true, false},
	} {
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "-test.run=^TestDriverFixture$", "lease-pipe-fixture", "ready:"+tc.line)
		g, e := startPrivate(context.Background(), cmd, 2*time.Second, time.Second, tc.dual)
		if g != nil {
			g.poison()
			if err := g.reaped(); err != nil {
				t.Fatal(err)
			}
		}
		if (e == nil) != tc.ok {
			t.Fatalf("dual=%v line=%s err=%v", tc.dual, tc.line, e)
		}
	}
}

// A real private child pipe catches protocol-state mistakes after READY2.
func TestDualPipeFixture(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "dual-pipe-fixture" {
		return
	}
	fmt.Println("READY2 ifindex=2 backend=4 program=3 horizon_ns=5000000000")
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		switch scan.Text() {
		case "CAPTURE":
			fmt.Println("TICKET 10 5000000010")
		case "ARM 10":
			fmt.Println("ARMED")
		case "DENY":
			fmt.Println("DENIED")
		default:
			os.Exit(2)
		}
	}
	os.Exit(0)
}
func TestDualCaptureArmDenyUsesUnchangedTicketProtocol(t *testing.T) {
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestDualPipeFixture$", "dual-pipe-fixture")
	g, e := startPrivate(context.Background(), cmd, 2*time.Second, time.Second, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		g.poison()
		if e := g.reaped(); e != nil {
			t.Error(e)
		}
	})
	ticket, e := g.Capture(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if ticket != (Ticket{Captured: 10, Deadline: 5000000010}) {
		t.Fatal(ticket)
	}
	if e = g.Arm(context.Background(), ticket); e != nil {
		t.Fatal(e)
	}
	if e = g.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
}
