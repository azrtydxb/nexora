package ai

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

func defaultResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// providerClient binds classification to each actual connection. Proxies are disabled because
// proxy-side DNS would bypass this check. Only the checked numeric address is dialled; HTTP Host
// and TLS ServerName remain the configured hostname, with normal certificate verification.
func providerClient(allowPublic bool, resolve Resolver, dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	if resolve == nil {
		resolve = defaultResolve
	}
	if dial == nil {
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		dial = d.DialContext
	}
	// Own the transport instead of inheriting process-global TLS or proxy overrides.
	tr := &http.Transport{
		ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
	}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addrs, err := endpointAddresses(ctx, host, allowPublic, resolve)
		if err != nil {
			return nil, err
		}
		// Do not re-resolve the hostname or silently fall back to another endpoint.
		return dial(ctx, network, net.JoinHostPort(addrs[0].String(), port))
	}
	return &http.Client{Transport: tr, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("%w: AI endpoint redirects are not allowed", ErrProvider)
	}}
}
