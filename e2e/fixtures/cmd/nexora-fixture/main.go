// Command nexora-fixture runs test fixture servers for Nexora's end-to-end tests.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const usage = "usage: nexora-fixture oidc --listen ADDR --client-id ID --client-secret-file F --users-file F"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var stop func()
	var err error
	switch os.Args[1] {
	case "oidc":
		stop, err = runOIDC(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexora-fixture %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	fmt.Println("fixture ready")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	<-sig
	stop()
}
