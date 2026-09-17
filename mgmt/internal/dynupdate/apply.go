// Package dynupdate applies RFC 2136 dynamic updates that engines received and forwarded over the
// control stream: TSIG is verified again here, the zone's update policy is enforced, and the
// prerequisites and update section are applied in one zone transaction with a serial bump.
package dynupdate

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/nzf"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

const (
	maxMessage = 65535
	maxTimer   = 2147483647
)

var tsigAlgorithms = map[string]string{"hmac-sha256": dns.HmacSHA256, "hmac-sha384": dns.HmacSHA384, "hmac-sha512": dns.HmacSHA512}

// Applier applies forwarded UPDATE messages. TSIGCheck (true in production) verifies the TSIG
// and the zone's update key list; tests of the update logic alone turn it off.
type Applier struct {
	Zones     *zone.Service
	TSIG      *tsigkey.Service
	Now       func() time.Time
	TSIGCheck bool
}

type rcodeError struct {
	rcode  int
	detail string
}

func (e *rcodeError) Error() string { return dns.RcodeToString[e.rcode] + ": " + e.detail }

func fail(rcode int, format string, args ...any) error {
	return &rcodeError{rcode: rcode, detail: fmt.Sprintf(format, args...)}
}

var errNoChange = errors.New("update changes nothing")

// Apply applies req forwarded by engineID and returns the DNS response code for the client.
func (a *Applier) Apply(ctx context.Context, engineID string, req *controlv1.UpdateRequest) *controlv1.UpdateResult {
	return a.ApplyFenced(ctx, engineID, req, nil)
}

// ApplyFenced checks stream ownership inside the zone mutation transaction, after
// read-only preparation. The fence and all zone/audit/snapshot writes share tx.
func (a *Applier) ApplyFenced(ctx context.Context, engineID string, req *controlv1.UpdateRequest, fence func(pgx.Tx) error) *controlv1.UpdateResult {
	res := &controlv1.UpdateResult{RequestId: req.RequestId}
	err := a.apply(ctx, engineID, req, fence)
	var re *rcodeError
	switch {
	case err == nil, errors.Is(err, errNoChange):
		res.Rcode = dns.RcodeSuccess
	case errors.As(err, &re):
		res.Rcode, res.Detail = uint32(re.rcode), re.detail
	default:
		slog.Error("dynamic update", "engine", engineID, "zone", req.Zone, "client", req.Client, "err", err)
		res.Rcode, res.Detail = dns.RcodeServerFailure, "internal error"
	}
	if res.Rcode != dns.RcodeSuccess {
		slog.Info("dynamic update not applied", "engine", engineID, "zone", req.Zone, "client", req.Client,
			"key", req.TsigKey, "rcode", dns.RcodeToString[int(res.Rcode)], "detail", res.Detail)
	}
	return res
}

func (a *Applier) apply(ctx context.Context, engineID string, req *controlv1.UpdateRequest, fence func(pgx.Tx) error) error {
	if len(req.Message) > maxMessage {
		return fail(dns.RcodeFormatError, "message too large")
	}
	msg := new(dns.Msg)
	if err := msg.Unpack(req.Message); err != nil {
		return fail(dns.RcodeFormatError, "malformed message: %v", err)
	}
	if msg.Opcode != dns.OpcodeUpdate || msg.Response || len(msg.Question) != 1 {
		return fail(dns.RcodeFormatError, "not an UPDATE with one zone")
	}
	q := msg.Question[0]
	if q.Qtype != dns.TypeSOA || q.Qclass != dns.ClassINET || dns.CanonicalName(q.Name) != dns.CanonicalName(req.Zone) {
		return fail(dns.RcodeFormatError, "zone section must be %s SOA IN", req.Zone)
	}
	z, err := a.Zones.GetZoneByName(ctx, q.Name)
	if errors.Is(err, store.ErrNotFound) {
		return fail(dns.RcodeNotAuth, "zone %s is not hosted", q.Name)
	}
	if err != nil {
		return err
	}
	if z.Kind != "primary" {
		return fail(dns.RcodeRefused, "zone %s is a secondary zone", z.Name)
	}
	keyName := dns.CanonicalName(req.TsigKey)
	if a.TSIGCheck {
		if err := a.verify(ctx, z, msg, req.Message, keyName); err != nil {
			return err
		}
	}
	actor := auth.Actor{Type: "system", ID: "tsig:" + keyName + "@" + engineID, Name: "tsig:" + keyName}
	_, err = a.Zones.MutateFenced(ctx, z.ID, fence, func(tx pgx.Tx, locked *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		opts := zone.RebuildOptions{}
		if locked.Kind != "primary" {
			return "", nil, nil, opts, fail(dns.RcodeRefused, "zone %s is a secondary zone", locked.Name)
		}
		if a.TSIGCheck && !a.allowed(ctx, tx, locked, keyName) {
			return "", nil, nil, opts, fail(dns.RcodeRefused, "key %s may not update %s", keyName, locked.Name)
		}
		records, err := zone.LoadRecords(ctx, tx, locked.ID)
		if err != nil {
			return "", nil, nil, opts, err
		}
		st, err := newState(locked, records)
		if err != nil {
			return "", nil, nil, opts, err
		}
		if err := st.prerequisites(msg.Answer); err != nil {
			return "", nil, nil, opts, err
		}
		if err := st.prescan(msg.Ns); err != nil {
			return "", nil, nil, opts, err
		}
		st.update(msg.Ns)
		deleted, added, err := st.changes()
		if err != nil {
			return "", nil, nil, opts, err
		}
		if len(deleted) == 0 && len(added) == 0 && !st.soaChanged {
			return "", nil, nil, opts, errNoChange
		}
		if err := zone.CheckSet(locked.Name, st.records()); err != nil {
			return "", nil, nil, opts, fail(dns.RcodeRefused, "%v", err)
		}
		if err := zone.ApplyRecordChanges(ctx, tx, locked.ID, deleted, added); err != nil {
			return "", nil, nil, opts, err
		}
		if st.soaChanged {
			s := st.soa
			if _, err := tx.Exec(ctx, `UPDATE zones SET soa_mname = $2, soa_rname = $3, soa_refresh = $4, soa_retry = $5, soa_expire = $6,
				soa_minimum = $7, soa_ttl = $8 WHERE id = $1`, locked.ID, dns.CanonicalName(s.Ns), dns.CanonicalName(s.Mbox),
				int64(s.Refresh), int64(s.Retry), int64(s.Expire), int64(s.Minttl), int64(s.Hdr.Ttl)); err != nil {
				return "", nil, nil, opts, err
			}
			serial := s.Serial
			opts.Serial = &serial
		}
		after := map[string]any{"client": req.Client, "deleted": len(deleted), "added": len(added), "soa_changed": st.soaChanged}
		return "dynamicUpdate", map[string]any{"serial": locked.Serial}, after, opts, nil
	}, actor)
	return err
}

// verify checks that raw carries a valid TSIG of keyName (the key the engine verified) and that
// the key may update z.
func (a *Applier) verify(ctx context.Context, z *zone.Zone, msg *dns.Msg, raw []byte, keyName string) error {
	ts := msg.IsTsig()
	if ts == nil {
		return fail(dns.RcodeRefused, "update is not TSIG-signed")
	}
	if dns.CanonicalName(ts.Hdr.Name) != keyName {
		return fail(dns.RcodeNotAuth, "TSIG key %s differs from the key the engine verified", ts.Hdr.Name)
	}
	if a.TSIG == nil {
		return fail(dns.RcodeNotAuth, "TSIG keys are not available")
	}
	var id uuid.UUID
	if err := a.Zones.Store.Pool.QueryRow(ctx, "SELECT id FROM tsig_keys WHERE name = $1", keyName).Scan(&id); err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			return fail(dns.RcodeNotAuth, "unknown TSIG key %s", keyName)
		}
		return store.MapError(err)
	}
	if !slices.Contains(z.UpdateTSIGKeyIDs, id) {
		return fail(dns.RcodeRefused, "key %s may not update %s", keyName, z.Name)
	}
	_, algorithm, secret, err := a.TSIG.Secret(ctx, a.Zones.Store.Pool, id)
	if err != nil {
		return err
	}
	secretB64 := base64.StdEncoding.EncodeToString(secret)
	clear(secret)
	if !strings.EqualFold(tsigAlgorithms[algorithm], ts.Algorithm) {
		return fail(dns.RcodeNotAuth, "TSIG algorithm %s does not match key %s", ts.Algorithm, keyName)
	}
	if err := dns.TsigVerify(raw, secretB64, "", false); err != nil {
		return fail(dns.RcodeNotAuth, "TSIG verification failed: %v", err)
	}
	return nil
}

// allowed re-checks the update policy on the locked zone row (the policy may have changed since verify).
func (a *Applier) allowed(ctx context.Context, tx pgx.Tx, z *zone.Zone, keyName string) bool {
	var ok bool
	err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM tsig_keys WHERE name = $1 AND id = ANY($2))", keyName, z.UpdateTSIGKeyIDs).Scan(&ok)
	return err == nil && ok
}

// state is the zone's RR data during one update: owner (lowercase) -> type -> RRs, SOA included.
type state struct {
	apex       string
	rrs        map[string]map[uint16][]dns.RR
	soa        *dns.SOA
	soaChanged bool
	original   map[string]dns.RR // recordKey -> stored record
}

func recordKey(rr dns.RR) (string, error) {
	w, err := nzf.FromRR(rr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%d|%x", strings.ToLower(rr.Header().Name), w.Type, w.RData), nil
}

func newState(z *zone.Zone, records []dns.RR) (*state, error) {
	st := &state{apex: dns.CanonicalName(z.Name), rrs: map[string]map[uint16][]dns.RR{}, original: map[string]dns.RR{}}
	st.soa = &dns.SOA{
		Hdr: dns.RR_Header{Name: st.apex, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: z.SOA.TTL},
		Ns:  z.SOA.MName, Mbox: z.SOA.RName, Serial: z.Serial,
		Refresh: z.SOA.Refresh, Retry: z.SOA.Retry, Expire: z.SOA.Expire, Minttl: z.SOA.Minimum,
	}
	st.add(st.soa)
	for _, rr := range records {
		k, err := recordKey(rr)
		if err != nil {
			return nil, err
		}
		st.original[k] = rr
		st.add(dns.Copy(rr))
	}
	return st, nil
}

func (st *state) add(rr dns.RR) {
	name := strings.ToLower(rr.Header().Name)
	if st.rrs[name] == nil {
		st.rrs[name] = map[uint16][]dns.RR{}
	}
	st.rrs[name][rr.Header().Rrtype] = append(st.rrs[name][rr.Header().Rrtype], rr)
}

func (st *state) inZone(name string) bool { return dns.IsSubDomain(st.apex, name) }

func (st *state) nameExists(name string) bool {
	for _, set := range st.rrs[name] {
		if len(set) > 0 {
			return true
		}
	}
	return false
}

func isMeta(t uint16) bool {
	return t == dns.TypeANY || t == dns.TypeAXFR || t == dns.TypeIXFR || t == dns.TypeMAILA || t == dns.TypeMAILB
}

// updatable are the types an update may name: operator-managed types and SOA, never DNSSEC data.
func updatable(t uint16) bool { return t == dns.TypeSOA || zone.ManagedTypes[t] }

// sameSet reports whether a and b hold the same RDATA sets (RFC 2136 §3.2.3).
func sameSet(a, b []dns.RR) bool {
	contains := func(set []dns.RR, rr dns.RR) bool {
		return slices.ContainsFunc(set, func(x dns.RR) bool { return dns.IsDuplicate(x, rr) })
	}
	for _, rr := range a {
		if !contains(b, rr) {
			return false
		}
	}
	for _, rr := range b {
		if !contains(a, rr) {
			return false
		}
	}
	return true
}

// prerequisites checks the prerequisite section (RFC 2136 §3.2).
func (st *state) prerequisites(prereqs []dns.RR) error {
	temp := map[string]map[uint16][]dns.RR{}
	for _, rr := range prereqs {
		h := rr.Header()
		name := strings.ToLower(h.Name)
		if h.Ttl != 0 {
			return fail(dns.RcodeFormatError, "prerequisite %s has a non-zero TTL", h.Name)
		}
		if !st.inZone(name) {
			return fail(dns.RcodeNotZone, "prerequisite %s is outside %s", h.Name, st.apex)
		}
		switch h.Class {
		case dns.ClassANY:
			if h.Rdlength != 0 {
				return fail(dns.RcodeFormatError, "prerequisite %s class ANY carries data", h.Name)
			}
			if h.Rrtype == dns.TypeANY {
				if !st.nameExists(name) {
					return fail(dns.RcodeNameError, "name %s is not in use", h.Name)
				}
			} else if len(st.rrs[name][h.Rrtype]) == 0 {
				return fail(dns.RcodeNXRrset, "RRset %s %s does not exist", h.Name, dns.TypeToString[h.Rrtype])
			}
		case dns.ClassNONE:
			if h.Rdlength != 0 {
				return fail(dns.RcodeFormatError, "prerequisite %s class NONE carries data", h.Name)
			}
			if h.Rrtype == dns.TypeANY {
				if st.nameExists(name) {
					return fail(dns.RcodeYXDomain, "name %s is in use", h.Name)
				}
			} else if len(st.rrs[name][h.Rrtype]) > 0 {
				return fail(dns.RcodeYXRrset, "RRset %s %s exists", h.Name, dns.TypeToString[h.Rrtype])
			}
		case dns.ClassINET:
			if temp[name] == nil {
				temp[name] = map[uint16][]dns.RR{}
			}
			temp[name][h.Rrtype] = append(temp[name][h.Rrtype], rr)
		default:
			return fail(dns.RcodeFormatError, "prerequisite %s has class %d", h.Name, h.Class)
		}
	}
	for name, types := range temp {
		for t, set := range types {
			if !sameSet(set, st.rrs[name][t]) {
				return fail(dns.RcodeNXRrset, "RRset %s %s differs", name, dns.TypeToString[t])
			}
		}
	}
	return nil
}

// prescan checks the update section before anything is applied (RFC 2136 §3.4.1).
func (st *state) prescan(updates []dns.RR) error {
	for _, rr := range updates {
		h := rr.Header()
		if !st.inZone(h.Name) {
			return fail(dns.RcodeNotZone, "update %s is outside %s", h.Name, st.apex)
		}
		switch h.Class {
		case dns.ClassINET:
			if isMeta(h.Rrtype) {
				return fail(dns.RcodeFormatError, "update adds meta type %d", h.Rrtype)
			}
		case dns.ClassANY:
			if h.Ttl != 0 || h.Rdlength != 0 || (h.Rrtype != dns.TypeANY && isMeta(h.Rrtype)) {
				return fail(dns.RcodeFormatError, "malformed RRset deletion at %s", h.Name)
			}
			if h.Rrtype == dns.TypeANY {
				continue
			}
		case dns.ClassNONE:
			if h.Ttl != 0 || isMeta(h.Rrtype) {
				return fail(dns.RcodeFormatError, "malformed RR deletion at %s", h.Name)
			}
		default:
			return fail(dns.RcodeFormatError, "update %s has class %d", h.Name, h.Class)
		}
		if !updatable(h.Rrtype) {
			return fail(dns.RcodeRefused, "type %s cannot be updated", dns.TypeToString[h.Rrtype])
		}
		if soa, ok := rr.(*dns.SOA); ok && h.Class == dns.ClassINET &&
			(soa.Refresh == 0 || soa.Retry == 0 || soa.Expire == 0 || max(soa.Refresh, soa.Retry, soa.Expire, soa.Minttl, h.Ttl) > maxTimer) {
			return fail(dns.RcodeRefused, "SOA timers out of range")
		}
	}
	return nil
}

// update applies the update section (RFC 2136 §3.4.2) to the in-memory state.
func (st *state) update(updates []dns.RR) {
	for _, rr := range updates {
		h := rr.Header()
		name := strings.ToLower(h.Name)
		apex := name == st.apex
		types := st.rrs[name]
		if types == nil {
			types = map[uint16][]dns.RR{}
			st.rrs[name] = types
		}
		switch h.Class {
		case dns.ClassINET:
			rr = dns.Copy(rr)
			rr.Header().Name = name
			if soa, ok := rr.(*dns.SOA); ok {
				if apex && zone.SerialLess(st.soa.Serial, soa.Serial) {
					st.soa, st.soaChanged = soa, true
					types[dns.TypeSOA] = []dns.RR{soa}
				}
				continue
			}
			if h.Rrtype == dns.TypeCNAME {
				if apex || slices.ContainsFunc(typesPresent(types), func(t uint16) bool { return t != dns.TypeCNAME }) {
					continue // §3.4.2.2: a CNAME joins no other data
				}
				types[dns.TypeCNAME] = nil // a CNAME replaces the existing one
			} else if len(types[dns.TypeCNAME]) > 0 {
				continue // no other data at a CNAME
			}
			set := types[h.Rrtype]
			i := slices.IndexFunc(set, func(x dns.RR) bool { return dns.IsDuplicate(x, rr) })
			if i >= 0 {
				set[i] = rr
			} else {
				set = append(set, rr)
			}
			for _, x := range set {
				x.Header().Ttl = h.Ttl
			}
			types[h.Rrtype] = set
		case dns.ClassANY:
			for t := range types {
				protected := apex && (t == dns.TypeSOA || t == dns.TypeNS)
				if (h.Rrtype == dns.TypeANY || t == h.Rrtype) && !protected {
					delete(types, t)
				}
			}
		case dns.ClassNONE:
			if h.Rrtype == dns.TypeSOA {
				continue
			}
			match := dns.Copy(rr)
			match.Header().Class = dns.ClassINET
			set := types[h.Rrtype]
			i := slices.IndexFunc(set, func(x dns.RR) bool { return dns.IsDuplicate(x, match) })
			if i < 0 || (apex && h.Rrtype == dns.TypeNS && len(set) == 1) {
				continue // absent, or the last apex NS
			}
			types[h.Rrtype] = slices.Delete(set, i, i+1)
		}
	}
}

func typesPresent(types map[uint16][]dns.RR) []uint16 {
	var out []uint16
	for t, set := range types {
		if len(set) > 0 {
			out = append(out, t)
		}
	}
	return out
}

// records returns every RR of the state except the SOA.
func (st *state) records() []dns.RR {
	var out []dns.RR
	for _, types := range st.rrs {
		for t, set := range types {
			if t != dns.TypeSOA {
				out = append(out, set...)
			}
		}
	}
	return out
}

// changes diffs the state against the stored records: deleted records and added or re-TTLed ones.
func (st *state) changes() (deleted, added []dns.RR, err error) {
	final := map[string]bool{}
	for _, rr := range st.records() {
		k, err := recordKey(rr)
		if err != nil {
			return nil, nil, fail(dns.RcodeFormatError, "record %s: %v", rr.Header().Name, err)
		}
		final[k] = true
		if old, ok := st.original[k]; !ok || old.Header().Ttl != rr.Header().Ttl {
			added = append(added, rr)
		}
	}
	for k, rr := range st.original {
		if !final[k] {
			deleted = append(deleted, rr)
		}
	}
	return deleted, added, nil
}
