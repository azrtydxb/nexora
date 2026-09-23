//go:build !linux

package main

import "os"

func main() {
	os.Stderr.WriteString("isolated active lab requires parent-owned Linux execution\n")
	os.Exit(1)
}
