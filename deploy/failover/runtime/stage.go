package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/piwi3910/nexora/deploy/failover/lease"
)

// StageOptions names immutable administrator-installed code, not a tenant
// manifest or command fragment. All imported source files are checked too.
type StageOptions struct {
	Python, Script string
	UID            uint32
	Timeout        time.Duration
}

type stageProcess struct {
	in, out  *os.File
	cmd      *exec.Cmd
	done     chan struct{}
	timeout  time.Duration
	nonce    string
	terminal bool
	serial   chan struct{}
	waitErr  error
}

func trusted(path string, uid uint32, executable bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("canonical absolute trusted path required")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return errors.New("trusted file unavailable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || (stat.Uid != uid && stat.Uid != 0) {
			return errors.New("mutable or foreign trusted path")
		}
		if p == path {
			if !info.Mode().IsRegular() || stat.Uid != uid || (executable && info.Mode().Perm()&0111 == 0) {
				return errors.New("invalid trusted file")
			}
		} else if !info.IsDir() {
			return errors.New("invalid trusted directory")
		}
		if p == "/" {
			return nil
		}
	}
}

func launchStage(ctx context.Context, o StageOptions) (*stageProcess, error) {
	if goruntime.GOOS != "linux" || uint32(os.Getuid()) != o.UID || uint32(os.Geteuid()) != o.UID || o.UID != 0 || o.Timeout < time.Second || o.Timeout > time.Minute || filepath.Base(o.Script) != "stage_session.py" {
		return nil, errors.New("explicit Linux root detached stage and bounded timeout required")
	}
	if err := trusted(o.Python, o.UID, true); err != nil {
		return nil, err
	}
	dir := filepath.Dir(o.Script)
	for _, path := range []string{o.Script, filepath.Join(dir, "linux_namespace_test.py"), filepath.Join(dir, "..", "adapter.py"), filepath.Join(dir, "..", "test_adapter.py")} {
		if err := trusted(path, o.UID, false); err != nil {
			return nil, err
		}
	}
	nonce, err := lease.NewHolderID()
	if err != nil {
		return nil, err
	}
	// Interpreter and script are root-owned (trusted() above); the argument list is fixed.
	cmd := exec.Command(o.Python, "-I", "-B", o.Script, "--execute-detached-stage") // nosemgrep: dangerous-exec-command
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.Dir = "/"
	return startStage(ctx, cmd, o.Timeout, nonce)
}

func startStage(ctx context.Context, cmd *exec.Cmd, timeout time.Duration, nonce string) (*stageProcess, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	cleanup := func() { inR.Close(); inW.Close(); outR.Close(); outW.Close() }
	if inW.SetWriteDeadline(time.Now().Add(timeout)) != nil || outR.SetReadDeadline(time.Now().Add(timeout)) != nil {
		cleanup()
		return nil, errors.New("stage pipes lack deadlines")
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = nil
	if err = cmd.Start(); err != nil {
		cleanup()
		return nil, errors.New("stage launch failed")
	}
	inR.Close()
	outW.Close()
	s := &stageProcess{in: inW, out: outR, cmd: cmd, done: make(chan struct{}), timeout: timeout, nonce: nonce, serial: make(chan struct{}, 1)}
	go func() { s.waitErr = cmd.Wait(); close(s.done) }()
	if err = s.exchange(ctx, "STAGE "+nonce, "STAGED "+nonce); err != nil {
		return nil, errors.Join(err, s.stop())
	}
	return s, nil
}

func line(f *os.File) (string, error) {
	var b strings.Builder
	var one [1]byte
	for b.Len() < 128 {
		n, e := f.Read(one[:])
		if e != nil {
			return "", e
		}
		if n != 1 {
			return "", io.ErrNoProgress
		}
		if one[0] == '\n' {
			return b.String(), nil
		}
		if one[0] < 32 || one[0] > 126 {
			return "", errors.New("invalid stage record")
		}
		b.WriteByte(one[0])
	}
	return "", errors.New("oversized stage record")
}

func (s *stageProcess) exchange(ctx context.Context, request, expected string) error {
	if s.terminal {
		return errors.New("stage connection terminal")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	// No recovery after any ambiguous record. Closing both descriptors interrupts
	// reads/writes even when cancellation has no deadline.
	stop := context.AfterFunc(ctx, func() { s.in.Close(); s.out.Close() })
	deadline, _ := ctx.Deadline()
	err := s.in.SetWriteDeadline(deadline)
	if err == nil {
		err = s.out.SetReadDeadline(deadline)
	}
	if err == nil {
		var n int
		n, err = io.WriteString(s.in, request+"\n")
		if err == nil && n != len(request)+1 {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		var reply string
		reply, err = line(s.out)
		if err == nil && reply != expected {
			err = errors.New("stale or invalid stage acknowledgement")
		}
	}
	if !stop() || ctx.Err() != nil || err != nil {
		s.terminal = true
		return errors.New("stage operation failed or ambiguous")
	}
	return nil
}

func (s *stageProcess) stop() error {
	s.terminal = true
	s.in.Close()
	s.out.Close()
	// Kill the exact owned process through os.Process, not a numeric process
	// group that might have been reaped/reused. unshare --kill-child arranges
	// parent-death termination of its private PID1 and all namespace children.
	_ = s.cmd.Process.Kill()
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case <-s.done:
		return nil
	case <-timer.C:
		return errors.New("stage reap deadline exceeded; cleanup uncertain")
	}
}

func (s *stageProcess) Close(ctx context.Context) error {
	select {
	case s.serial <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.serial }()
	if err := s.exchange(ctx, "CLEAN "+s.nonce, "CLEANED "+s.nonce); err != nil {
		return errors.Join(err, s.stop())
	}
	s.terminal = true
	s.in.Close()
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), s.stop())
	case <-timer.C:
		return errors.Join(errors.New("stage exit deadline"), s.stop())
	case <-s.done:
		var extra [1]byte
		readCtx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		deadline, _ := readCtx.Deadline()
		stop := context.AfterFunc(readCtx, func() { s.out.Close() })
		defer stop()
		_ = s.out.SetReadDeadline(deadline)
		n, end := s.out.Read(extra[:])
		s.out.Close()
		if n != 0 || end != io.EOF || readCtx.Err() != nil {
			return errors.New("trailing stage output or inherited open pipe")
		}
		if s.waitErr != nil {
			return errors.New("stage cleanup process failed")
		}
		return nil
	}
}
