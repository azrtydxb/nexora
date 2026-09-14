package zone

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/miekg/dns"
)

// DiffForTest compares a served RR set with the stored records, ignoring the SOA (records carry
// none) and RRSIGs, and returns the differing lines prefixed with "-" (served only) or "+"
// (records only).
func DiffForTest(served, records []dns.RR) string {
	lines := func(rrs []dns.RR) map[string]bool {
		out := map[string]bool{}
		for _, rr := range rrs {
			if t := rr.Header().Rrtype; t != dns.TypeSOA && t != dns.TypeRRSIG {
				out[rr.String()] = true
			}
		}
		return out
	}
	s, r := lines(served), lines(records)
	var diff []string
	for l := range s {
		if !r[l] {
			diff = append(diff, "- "+l)
		}
	}
	for l := range r {
		if !s[l] {
			diff = append(diff, "+ "+l)
		}
	}
	slices.Sort(diff)
	return strings.Join(diff, "\n")
}

// RebuildWithImageForTest rebuilds zone zoneID from its stored records and writes a full image of
// the resulting version, so the next versions are journal deltas without an image due.
func RebuildWithImageForTest(t *testing.T, pool *pgxpool.Pool, zoneID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	z, err := loadZone(ctx, tx, zoneID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Rebuild(ctx, tx, nil, z, RebuildOptions{Force: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if z.ImageSeq != z.CurrentSeq {
		desired, err := desiredRRs(ctx, tx, z)
		if err != nil {
			t.Fatal(err)
		}
		recs, err := toRecords(desired)
		if err != nil {
			t.Fatal(err)
		}
		origin, err := wireName(z.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeImage(ctx, tx, z, origin, z.CurrentSeq, z.Serial, recs); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE zones SET image_seq = $2 WHERE id = $1`, z.ID, z.ImageSeq); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
