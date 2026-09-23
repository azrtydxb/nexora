package xfrin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
	"github.com/piwi3910/nexora/mgmt/internal/zonemd"
)

const (
	// maxTransferRRs bounds the records one transfer may deliver.
	maxTransferRRs = zone.MaxImportRecords
	// transferReadTimeout bounds the wait for each transfer message; transferDeadline the whole transfer.
	transferReadTimeout = 30 * time.Second
	transferDeadline    = 10 * time.Minute
	// retryBeforeLoad is the retry interval of a zone that never loaded (no SOA retry known yet).
	retryBeforeLoad = 600 * time.Second
	// minTimer keeps a primary's tiny SOA refresh/retry from turning into a busy loop.
	minTimer      = 5
	maxTimer      = 2147483647
	maxErrorBytes = 1024
)

var systemActor = auth.Actor{Type: "system", ID: "xfrin", Name: "system:xfrin"}

var tsigAlgorithms = map[string]string{"hmac-sha256": dns.HmacSHA256, "hmac-sha384": dns.HmacSHA384, "hmac-sha512": dns.HmacSHA512}

// Refresher checks a secondary zone's primaries and transfers and applies newer versions.
type Refresher struct {
	Store *store.Store
	Zones *zone.Service
	TSIG  *tsigkey.Service
	Now   func() time.Time
	Dial  time.Duration
}

type tsigKey struct{ name, algorithm, secretB64 string }

func (r *Refresher) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// Refresh brings secondary zone zoneID up to date from the first primary that answers. trigger
// ("timer", "notify", "manual" or "create") is recorded on success. When every primary fails the
// zone retries after its SOA retry interval and expires once its SOA expire time has passed.
func (r *Refresher) Refresh(ctx context.Context, zoneID uuid.UUID, trigger string) error {
	z, err := r.Zones.GetZone(ctx, zoneID)
	if err != nil {
		return err
	}
	if z.Kind != "secondary" {
		return fmt.Errorf("zone %s is not a secondary zone", z.Name)
	}
	// Requests arriving while this refresh runs keep the zone due (compared when the result is written).
	var requests int64
	if err := r.Store.Pool.QueryRow(ctx, "SELECT refresh_requests FROM zones WHERE id = $1", zoneID).Scan(&requests); err != nil {
		return store.MapError(err)
	}
	var errs []error
	for _, p := range z.Primaries {
		err := r.fromPrimary(ctx, z, p, trigger, requests)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		errs = append(errs, fmt.Errorf("primary %s: %w", p.Address, err))
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no primaries configured"))
	}
	cause := errors.Join(errs...)
	if err := r.fail(ctx, z, cause, requests); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (r *Refresher) fromPrimary(ctx context.Context, z *zone.Zone, p zone.Endpoint, trigger string, requests int64) error {
	var key *tsigKey
	if p.TSIGKeyID != nil {
		if r.TSIG == nil {
			return errors.New("TSIG key storage is not available")
		}
		name, algorithm, secret, err := r.TSIG.Secret(ctx, r.Store.Pool, *p.TSIGKeyID)
		if err != nil {
			return err
		}
		key = &tsigKey{name: name, algorithm: tsigAlgorithms[algorithm], secretB64: base64.StdEncoding.EncodeToString(secret)}
		clear(secret)
	}
	remote, err := r.querySOA(ctx, z.Name, p.Address, key)
	if err != nil {
		return fmt.Errorf("SOA query: %w", err)
	}
	if z.Loaded && !zone.SerialLess(z.Serial, remote.Serial) {
		return r.succeed(ctx, z, nil, remote, trigger, requests)
	}
	var ans *Answer
	var soa *dns.SOA
	if z.Loaded {
		// Any IXFR failure (NOTIMP, FORMERR, an unusable answer stream) retries once with AXFR.
		if ans, soa, err = r.transfer(ctx, z, p.Address, key, true); err != nil {
			ans, soa, err = r.transfer(ctx, z, p.Address, key, false)
		}
	} else {
		ans, soa, err = r.transfer(ctx, z, p.Address, key, false)
	}
	if err != nil {
		return err
	}
	if ans.UpToDate {
		return r.succeed(ctx, z, nil, soa, trigger, requests)
	}
	return r.succeed(ctx, z, ans, soa, trigger, requests)
}

func (r *Refresher) querySOA(ctx context.Context, name, addr string, key *tsigKey) (*dns.SOA, error) {
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypeSOA)
	m.RecursionDesired = false
	c := &dns.Client{Net: "udp", Timeout: r.Dial}
	if key != nil {
		m.SetTsig(key.name, key.algorithm, 300, r.now().Unix())
		c.TsigSecret = map[string]string{key.name: key.secretB64}
	}
	resp, _, err := c.ExchangeContext(ctx, m, addr)
	if err == nil && resp.Truncated {
		c.Net = "tcp"
		resp, _, err = c.ExchangeContext(ctx, m, addr)
	}
	if err != nil {
		return nil, err
	}
	if key != nil && resp.IsTsig() == nil {
		return nil, errors.New("response is not TSIG-signed")
	}
	if resp.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("rcode %s", dns.RcodeToString[resp.Rcode])
	}
	for _, rr := range resp.Answer {
		if soa, ok := rr.(*dns.SOA); ok && strings.EqualFold(soa.Hdr.Name, name) {
			return soa, nil
		}
	}
	return nil, errors.New("answer holds no SOA for the zone")
}

// transfer runs an IXFR (from the zone's serial) or AXFR over TCP and interprets the stream.
func (r *Refresher) transfer(ctx context.Context, z *zone.Zone, addr string, key *tsigKey, ixfr bool) (*Answer, *dns.SOA, error) {
	kind := "AXFR"
	m := new(dns.Msg)
	if ixfr {
		kind = "IXFR"
		m.SetIxfr(z.Name, z.Serial, z.SOA.MName, z.SOA.RName)
	} else {
		m.SetAxfr(z.Name)
	}
	d := net.Dialer{Timeout: r.Dial}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", kind, err)
	}
	// Closing the connection ends the transfer goroutine, which then closes the channel.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.AfterFunc(transferDeadline, func() { _ = conn.Close() })
	defer deadline.Stop()
	tr := &dns.Transfer{Conn: &dns.Conn{Conn: conn}, ReadTimeout: transferReadTimeout}
	if key != nil {
		// With a secret set, every message must carry a valid TSIG (dns.Transfer.ReadMsg).
		m.SetTsig(key.name, key.algorithm, 300, r.now().Unix())
		tr.TsigSecret = map[string]string{key.name: key.secretB64}
	}
	ch, err := tr.In(m, addr)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("%s: %w", kind, err)
	}
	var rrs []dns.RR
	for env := range ch {
		if env.Error != nil {
			err = env.Error
			break
		}
		rrs = append(rrs, env.RR...)
		if len(rrs) > maxTransferRRs {
			err = fmt.Errorf("more than %d records", maxTransferRRs)
			break
		}
	}
	if err != nil {
		_ = conn.Close()
		for range ch {
		}
		return nil, nil, fmt.Errorf("%s: %w", kind, err)
	}
	ans, err := Interpret(rrs, z.Serial, ixfr)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", kind, err)
	}
	for _, rr := range rrs {
		h := rr.Header()
		if h.Class != dns.ClassINET || !dns.IsSubDomain(z.Name, h.Name) {
			return nil, nil, fmt.Errorf("%s: record %s outside zone %s", kind, h.Name, z.Name)
		}
		if soa, ok := rr.(*dns.SOA); ok && !strings.EqualFold(soa.Hdr.Name, z.Name) {
			return nil, nil, fmt.Errorf("%s: SOA at %s is not at the zone apex", kind, soa.Hdr.Name)
		}
	}
	return ans, rrs[0].(*dns.SOA), nil
}

func timer(v uint32) int64 { return min(max(int64(v), minTimer), maxTimer) }

// succeed applies ans (nil: already up to date) and records the timers of the primary's soa.
func (r *Refresher) succeed(ctx context.Context, z *zone.Zone, ans *Answer, soa *dns.SOA, trigger string, requests int64) error {
	now := r.now()
	refresh, expire := timer(soa.Refresh), timer(soa.Expire)
	timers := func(q zone.Execer) error {
		_, err := q.Exec(ctx, `UPDATE zones SET loaded = true, expired = false, last_refresh_at = $2, last_success_at = $2,
			next_refresh_at = CASE WHEN refresh_requests <> $3 THEN now() ELSE $4 END, expires_at = $5, last_error = '', last_trigger = $6,
			refresh_trigger = CASE WHEN refresh_requests <> $3 THEN refresh_trigger ELSE '' END WHERE id = $1`,
			z.ID, now, requests, now.Add(time.Duration(refresh)*time.Second), now.Add(time.Duration(expire)*time.Second), trigger)
		return err
	}
	if ans == nil && !z.Expired {
		return store.MapError(timers(r.Store.Pool))
	}
	// A transfer is applied only when the zone it leads to passes the zone's ZONEMD verification.
	var verified zonemd.Result
	if ans != nil {
		set, err := r.transferred(ctx, z, ans, soa)
		if err != nil {
			return err
		}
		verified = zonemd.Verify(z.Name, set, zonemd.Mode(z.ZonemdVerify))
		if verified.Status == zonemd.StatusFailed {
			if err := zone.SetZonemdStatus(ctx, r.Store.Pool, z.ID, string(verified.Status), verified.Err); err != nil {
				return err
			}
			return fmt.Errorf("zonemd: %s", verified.Err)
		}
	}
	// A transfer, or a zone that expired and answers again: publish a new config version.
	_, err := r.Zones.Mutate(ctx, z.ID, func(tx pgx.Tx, locked *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		after := map[string]any{"trigger": trigger, "serial": soa.Serial}
		opts := zone.RebuildOptions{}
		if ans != nil {
			switch {
			case ans.Full != nil:
				after["transfer"] = "axfr"
				if err := zone.SetRecords(ctx, tx, locked.ID, ans.Full); err != nil {
					return "", nil, nil, opts, err
				}
			case locked.Serial != ans.Diffs[0].FromSerial:
				return "", nil, nil, opts, fmt.Errorf("zone %s changed during the transfer (serial %d, IXFR from %d): %w",
					locked.Name, locked.Serial, ans.Diffs[0].FromSerial, store.ErrConflict)
			default:
				after["transfer"] = "ixfr"
				for _, d := range ans.Diffs {
					if err := zone.ApplyRecordChanges(ctx, tx, locked.ID, d.Deleted, d.Added); err != nil {
						return "", nil, nil, opts, err
					}
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE zones SET soa_mname = $2, soa_rname = $3, soa_refresh = $4, soa_retry = $5,
				soa_expire = $6, soa_minimum = $7, soa_ttl = $8 WHERE id = $1`, locked.ID, strings.ToLower(soa.Ns), strings.ToLower(soa.Mbox),
				refresh, timer(soa.Retry), expire, min(int64(soa.Minttl), maxTimer), min(int64(soa.Hdr.Ttl), maxTimer)); err != nil {
				return "", nil, nil, opts, err
			}
			if err := zone.SetZonemdStatus(ctx, tx, locked.ID, string(verified.Status), verified.Err); err != nil {
				return "", nil, nil, opts, err
			}
			serial := ans.Serial
			opts.Serial = &serial
		}
		return "refreshZone", map[string]any{"serial": locked.Serial}, after, opts, timers(tx)
	}, systemActor)
	return err
}

// transferred returns the zone content ans leads to: the AXFR records (SOA first), or the served
// zone with the IXFR diffs applied and soa, the primary's current SOA, as its SOA.
func (r *Refresher) transferred(ctx context.Context, z *zone.Zone, ans *Answer, soa *dns.SOA) ([]dns.RR, error) {
	if ans.Full != nil {
		return ans.Full, nil
	}
	tx, err := r.Store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, store.MapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := zone.LoadServed(ctx, tx, z)
	if err != nil {
		return nil, fmt.Errorf("zone %s served records: %w", z.Name, err)
	}
	return append(applyDiffs(current, ans.Diffs), soa), nil
}

// applyDiffs applies IXFR diffs to current the way zone.ApplyRecordChanges applies them to the
// stored records: a deletion matches owner (case-insensitively), type and RDATA whatever the
// TTL, an added record replaces an equal one, every RRset written takes the TTL of its last added
// record, and SOA records are dropped.
func applyDiffs(current []dns.RR, diffs []Diff) []dns.RR {
	key := func(rr dns.RR) string {
		c := dns.Copy(rr)
		h := c.Header()
		h.Name, h.Ttl = strings.ToLower(h.Name), 0
		return c.String()
	}
	type rrset struct {
		owner string
		rtype uint16
	}
	set := make(map[string]dns.RR, len(current))
	for _, rr := range current {
		if rr.Header().Rrtype != dns.TypeSOA {
			set[key(rr)] = rr
		}
	}
	ttls := map[rrset]uint32{}
	for _, d := range diffs {
		for _, rr := range d.Deleted {
			delete(set, key(rr))
		}
		for _, rr := range d.Added {
			h := rr.Header()
			if h.Rrtype == dns.TypeSOA {
				continue
			}
			set[key(rr)] = rr
			ttls[rrset{strings.ToLower(h.Name), h.Rrtype}] = h.Ttl
		}
	}
	out := make([]dns.RR, 0, len(set)+1)
	for _, rr := range set {
		h := rr.Header()
		if ttl, ok := ttls[rrset{strings.ToLower(h.Name), h.Rrtype}]; ok && ttl != h.Ttl {
			rr = dns.Copy(rr)
			rr.Header().Ttl = ttl
		}
		out = append(out, rr)
	}
	return out
}

// fail records a failed refresh: retry after the SOA retry interval and expire when due.
func (r *Refresher) fail(ctx context.Context, z *zone.Zone, cause error, requests int64) error {
	now := r.now()
	retry := retryBeforeLoad
	if z.Loaded {
		retry = time.Duration(z.SOA.Retry) * time.Second
	}
	msg := cause.Error()
	msg = strings.ToValidUTF8(msg[:min(len(msg), maxErrorBytes)], "")
	update := func(q zone.Execer) error {
		_, err := q.Exec(ctx, `UPDATE zones SET last_refresh_at = $2, last_error = $3,
			next_refresh_at = CASE WHEN refresh_requests <> $4 THEN now() ELSE $5 END,
			refresh_trigger = CASE WHEN refresh_requests <> $4 THEN refresh_trigger ELSE '' END,
			expired = expired OR (loaded AND expires_at IS NOT NULL AND expires_at <= $2) WHERE id = $1`,
			z.ID, now, msg, requests, now.Add(retry))
		return err
	}
	if z.Expired || !z.Loaded || z.ExpiresAt == nil || z.ExpiresAt.After(now) {
		return store.MapError(update(r.Store.Pool))
	}
	// The zone expires now: publish, so engines answer SERVFAIL for it.
	_, err := r.Zones.Mutate(ctx, z.ID, func(tx pgx.Tx, _ *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		return "expireZone", nil, map[string]any{"error": msg}, zone.RebuildOptions{}, update(tx)
	}, systemActor)
	return err
}
