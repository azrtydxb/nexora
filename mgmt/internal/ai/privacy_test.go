package ai

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func fakeResolve(m map[string][]string) Resolver {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, s := range m[host] {
			out = append(out, netip.MustParseAddr(s))
		}
		if len(out) == 0 {
			return nil, errors.New("no such host")
		}
		return out, nil
	}
}

func TestEndpointPrivacyGuard(t *testing.T) {
	r := fakeResolve(map[string][]string{"localhost": {"127.0.0.1", "::1"}, "api.openai.com": {"162.159.140.245"},
		"mixed.test": {"10.0.0.5", "8.8.8.8"}, "fastllm.lan": {"192.168.10.125"}})
	for _, u := range []string{"http://192.168.10.125:4000/v1", "http://localhost:1234/v1", "http://[fd00::1]/v1", "http://fastllm.lan/v1/", "http://100.64.1.1/v1"} {
		if err := CheckEndpoint(context.Background(), u, false, r); err != nil {
			t.Errorf("%s rejected: %v", u, err)
		}
	}
	for _, u := range []string{"https://api.openai.com/v1", "http://mixed.test/v1", "http://8.8.8.8/v1"} {
		if err := CheckEndpoint(context.Background(), u, false, r); !errors.Is(err, ErrEndpointNotPrivate) {
			t.Errorf("%s: %v, want ErrEndpointNotPrivate", u, err)
		}
		if err := CheckEndpoint(context.Background(), u, true, r); err != nil {
			t.Errorf("%s with allowPublic: %v", u, err)
		}
	}
	if err := CheckEndpoint(context.Background(), "http://unknown.test/v1", false, r); !errors.Is(err, ErrProvider) {
		t.Errorf("unresolvable host: %v, want ErrProvider", err)
	}
}
