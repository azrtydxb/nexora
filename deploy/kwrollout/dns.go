package kwrollout

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// DNSProbe checks a known, direct A/AAAA record, not an arbitrary recursive name.
// Address must be a literal IP:port so a broken system resolver cannot redirect
// or prevent the probe. Expected comes from configuration, never the first reply.
type DNSProbe struct {
	Address  string
	Name     string
	Expected netip.Addr
}

// DNSSample records each attempted transport independently, including failures.
// Callbacks run synchronously; they must not block or initiate rollout mutations.
type DNSSample struct {
	Address, Transport string
	Started            time.Time
	Duration           time.Duration
	Err                error
}

// CheckDNS verifies every target over UDP and TCP without retries or fallback.
// The first observed failure stops the round, but is still delivered to record.
func CheckDNS(ctx context.Context, probes []DNSProbe, record func(DNSSample)) error {
	if len(probes) == 0 {
		return fmt.Errorf("DNS probe set must not be empty")
	}
	for _, p := range probes {
		if err := p.validate(); err != nil {
			return err
		}
	}
	for _, p := range probes {
		for _, transport := range []string{"udp", "tcp"} {
			if err := ctx.Err(); err != nil {
				return err
			}
			sample := DNSSample{Address: p.Address, Transport: transport, Started: time.Now()}
			sample.Err = p.query(ctx, transport)
			sample.Duration = time.Since(sample.Started)
			if record != nil {
				record(sample)
			}
			if sample.Err != nil {
				return fmt.Errorf("DNS %s %s: %w", p.Address, transport, sample.Err)
			}
		}
	}
	return ctx.Err()
}

// MonitorDNS checks immediately and then periodically until cancellation or the
// first failed sample. The rollout adapter must cancel progression on its error
// and join the monitor before releasing the deployment lock.
func MonitorDNS(ctx context.Context, probes []DNSProbe, interval time.Duration, record func(DNSSample)) error {
	if interval <= 0 {
		return fmt.Errorf("DNS probe interval must be positive")
	}
	for {
		if err := CheckDNS(ctx, probes, record); err != nil {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (p DNSProbe) validate() error {
	address, err := netip.ParseAddrPort(p.Address)
	if err != nil || address.Port() == 0 || address.Addr().Zone() != "" || address.Addr().IsUnspecified() || address.Addr().IsMulticast() {
		return fmt.Errorf("DNS probe requires a literal IP and nonzero port: %q", p.Address)
	}
	if !p.Expected.IsValid() || p.Expected.Zone() != "" {
		return fmt.Errorf("DNS probe requires an expected IP address")
	}
	if _, ok := dns.IsDomainName(p.Name); !ok || p.Name == "" || p.Name == "." || strings.TrimSpace(p.Name) != p.Name {
		return fmt.Errorf("DNS probe requires a valid record name")
	}
	return nil
}

func (p DNSProbe) query(ctx context.Context, transport string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	expected := p.Expected.Unmap()
	qtype := uint16(dns.TypeA)
	if expected.Is6() {
		qtype = dns.TypeAAAA
	}
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(p.Name), qtype)
	client := &dns.Client{Net: transport, Timeout: 2 * time.Second}
	conn, err := client.DialContext(ctx, p.Address)
	if err != nil {
		return err
	}
	defer conn.Close()
	// miekg/dns obeys context deadlines but does not interrupt an already
	// pending read when a deadline-free context is cancelled. Close explicitly.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	reply, _, err := client.ExchangeWithConnContext(ctx, q, conn)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if reply == nil || !reply.Response || reply.Id != q.Id || reply.Opcode != dns.OpcodeQuery || reply.Truncated || reply.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("unsuccessful, truncated or mismatched DNS response")
	}
	if len(reply.Question) != 1 || !strings.EqualFold(reply.Question[0].Name, q.Question[0].Name) || reply.Question[0].Qtype != qtype || reply.Question[0].Qclass != dns.ClassINET {
		return fmt.Errorf("DNS response question differs from probe")
	}
	if len(reply.Answer) == 0 {
		return fmt.Errorf("DNS response has no expected address")
	}
	for _, rr := range reply.Answer {
		if !strings.EqualFold(rr.Header().Name, q.Question[0].Name) || rr.Header().Class != dns.ClassINET || rr.Header().Rrtype != qtype {
			return fmt.Errorf("DNS response contains an unexpected record")
		}
		var address netip.Addr
		switch rr := rr.(type) {
		case *dns.A:
			address, _ = netip.AddrFromSlice(rr.A)
		case *dns.AAAA:
			address, _ = netip.AddrFromSlice(rr.AAAA)
		}
		if address.Unmap() != expected {
			return fmt.Errorf("DNS response address differs from configured expectation")
		}
	}
	return nil
}
