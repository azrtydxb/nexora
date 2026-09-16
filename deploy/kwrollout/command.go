package kwrollout

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// One injectable process boundary lets tests exercise the complete production
// assembly without replacing individual safety gates or writing to a cluster.
type commandSpec struct {
	Program   string
	Args, Env []string
	Input     []byte
}

type commandRunner func(context.Context, commandSpec) ([]byte, error)

func executeCommand(ctx context.Context, spec commandSpec) ([]byte, error) {
	cmd := exec.CommandContext(ctx, spec.Program, spec.Args...)
	cmd.Stdin = bytes.NewReader(spec.Input)
	cmd.Env = spec.Env
	cmd.WaitDelay = time.Second
	return cmd.Output() // Never forward stderr (plugins/Helm may include Secrets).
}
