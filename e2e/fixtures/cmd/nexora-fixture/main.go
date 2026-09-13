// Command nexora-fixture runs test fixture servers for Nexora's end-to-end tests.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const usage = `usage (a port of 0 lets the kernel choose; the READY line names the bound addresses):
  nexora-fixture dns --udp ADDR --tcp ADDR --dot ADDR --doh ADDR --control ADDR --cert-dir DIR
  nexora-fixture http --listen ADDR
  nexora-fixture oidc --listen ADDR --client-id ID --client-secret-file F --users-file F`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var stop func()
	var addrs string
	var err error
	switch os.Args[1] {
	case "dns":
		stop, addrs, err = runDNS(os.Args[2:])
	case "http":
		stop, addrs, err = runHTTP(os.Args[2:])
	case "oidc":
		stop, addrs, err = runOIDC(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexora-fixture %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	// One machine-readable line with every bound address, so callers can listen on port 0.
	fmt.Println("READY " + addrs)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	<-sig
	stop()
}
