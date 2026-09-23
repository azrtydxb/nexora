package main

import (
	"fmt"
	"net"
	"strconv"
)

// Ports are taken from the original connection, never from DNS answer text.
type socketTuple struct {
	LocalIP    string `json:"local_ip"`
	LocalPort  int    `json:"local_port"`
	RemoteIP   string `json:"remote_ip"`
	RemotePort int    `json:"remote_port"`
}

func captureSocket(local, remote net.Addr) (socketTuple, error) {
	var tuple socketTuple
	for i, addr := range []net.Addr{local, remote} {
		if addr == nil {
			return tuple, fmt.Errorf("missing probe socket address")
		}
		host, port, err := net.SplitHostPort(addr.String())
		if err != nil {
			return tuple, err
		}
		ip := net.ParseIP(host)
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || ip == nil || ip.To4() == nil {
			return tuple, fmt.Errorf("invalid probe socket address %q", addr)
		}
		if i == 0 {
			tuple.LocalIP, tuple.LocalPort = ip.String(), n
		} else {
			tuple.RemoteIP, tuple.RemotePort = ip.String(), n
		}
	}
	return tuple, nil
}
