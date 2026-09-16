package kwrollout

import (
	"context"
	"fmt"
	"time"
)

func kubectlCommand(kubeContext, namespace string) func(context.Context, []byte, ...string) ([]byte, error) {
	return kubectlWith(kubeContext, namespace, executeCommand)
}

func kubectlWith(kubeContext, namespace string, execute commandRunner) func(context.Context, []byte, ...string) ([]byte, error) {
	return func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		if kubeContext == "" || namespace == "" {
			return nil, fmt.Errorf("explicit Kubernetes context and namespace required")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		prefix := []string{"--context", kubeContext, "--namespace", namespace, "--request-timeout=15s"}
		out, err := execute(ctx, commandSpec{Program: "kubectl", Args: append(prefix, args...), Input: input})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Do not expose credential-plugin diagnostics or authentication material.
		if err != nil {
			return out, fmt.Errorf("kubectl operation failed: %w", err)
		}
		return out, nil
	}
}
