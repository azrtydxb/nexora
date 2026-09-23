// fg-runtime is a parent-invoked detached lab executable, never a service.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	fgruntime "github.com/piwi3910/nexora/deploy/failover/runtime"
)

func main() {
	path := flag.String("config", "", "explicit administrator-owned lab JSON")
	enabled := flag.Bool("execute-detached-lab", false, "opt in to disposable namespaces and Lease writes")
	flag.Parse()
	if !*enabled || *path == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "requires --execute-detached-lab --config; no frontend activation supported")
		os.Exit(2)
	}
	cfg, err := fgruntime.LoadConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	report, err := fgruntime.Execute(ctx, cfg)
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
