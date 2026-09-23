package harness

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// CommandIn runs a bounded command in this lab (ns == "" selects its root).
// It is for namespace setup and one-shot probes; StartIn owns long-lived children.
func (l *NetLab) CommandIn(ns, name string, args ...string) string {
	l.e.T.Helper()
	if !InNetLab() {
		l.e.T.Fatal("CommandIn outside the network namespace lab")
	}
	if ns != "" {
		pid, ok := l.pid[ns]
		if !ok {
			l.e.T.Fatalf("unknown lab namespace %q", ns)
		}
		args = append([]string{"-t", strconv.Itoa(pid), "-n", "--", name}, args...)
		name = "nsenter"
	}
	ctx, cancel := context.WithTimeout(l.e.T.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) // nosemgrep: dangerous-exec-command
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		l.e.T.Fatalf("lab %q: %s %v: %v (context: %v)\n%s", ns, name, args, err, ctx.Err(), out)
	}
	return string(out)
}
