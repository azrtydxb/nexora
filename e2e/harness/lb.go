package harness

import (
	"io"
	"net"
	"sync"
	"time"
)

// Balancer is an in-process TCP load balancer on loopback.
type Balancer struct {
	Addr string
}

// StartTCPBalancer listens on a free loopback port; each accepted connection dials backends in
// order (200 ms timeout each) and pipes bytes both ways until either side closes. The listener
// and every open connection are closed when the test ends.
func (e *Env) StartTCPBalancer(backends ...string) *Balancer {
	e.T.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.T.Fatal(err)
	}
	var mu sync.Mutex
	open := map[net.Conn]struct{}{}
	track := func(c net.Conn, add bool) {
		mu.Lock()
		defer mu.Unlock()
		if add {
			open[c] = struct{}{}
		} else {
			delete(open, c)
		}
	}
	e.T.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for c := range open {
			_ = c.Close()
		}
	})
	go func() {
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				var backend net.Conn
				for _, addr := range backends {
					if backend, err = net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
						break
					}
				}
				if backend == nil {
					_ = client.Close()
					return
				}
				track(client, true)
				track(backend, true)
				done := make(chan struct{}, 2)
				pipe := func(dst, src net.Conn) {
					_, _ = io.Copy(dst, src)
					done <- struct{}{}
				}
				go pipe(backend, client)
				go pipe(client, backend)
				<-done // either direction ending tears down both, so a dead backend drops its clients
				_ = client.Close()
				_ = backend.Close()
				track(client, false)
				track(backend, false)
			}()
		}
	}()
	return &Balancer{Addr: l.Addr().String()}
}
