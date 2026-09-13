// Command nexora-mgmt is the Nexora management plane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const usage = "usage: nexora-mgmt serve | migrate | ca init --out <dir> | user create --admin"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var err error
	switch {
	case len(args) == 1 && args[0] == "migrate":
		err = migrate(ctx, stdout)
	case len(args) >= 2 && args[0] == "ca" && args[1] == "init":
		err = caInit(args[2:], stdout)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "nexora-mgmt %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

// migrate needs only the database, so it reads NEXORA_DATABASE_URL directly instead of
// config.Load (which also requires the CA files).
func migrate(ctx context.Context, stdout io.Writer) error {
	url := os.Getenv("NEXORA_DATABASE_URL")
	if url == "" {
		return errors.New("NEXORA_DATABASE_URL is required")
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "migrations applied")
	return nil
}

func caInit(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ca init", flag.ContinueOnError)
	out := fs.String("out", "", "directory for ca.crt and ca.key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() != 0 {
		return errors.New("usage: nexora-mgmt ca init --out <dir>")
	}
	if err := pki.InitCA(*out); err != nil {
		return err
	}
	ca, err := pki.LoadCA(*out+"/ca.crt", *out+"/ca.key")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ca fingerprint: %s\n", ca.Fingerprint())
	return nil
}
