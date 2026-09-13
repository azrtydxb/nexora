package zone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/zonefile"
)

// MaxImportRecords bounds the records of one imported zone file.
const MaxImportRecords = 1000000

// ImportResult is the zone after an import and how many records it holds.
type ImportResult struct {
	Zone            *Zone
	RecordsImported int
}

// Import replaces every record and the SOA fields of primary zone zoneID (at revision) with the
// zone file content. The file's SOA serial is adopted when RFC 1982-greater than the current one.
func (s *Service) Import(ctx context.Context, actor auth.Actor, zoneID uuid.UUID, revision int64, content string) (*ImportResult, error) {
	current, err := s.GetZone(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	res, err := zonefile.Parse(strings.NewReader(content), current.Name, zonefile.Options{AllowedTypes: ManagedTypes, MaxRecords: MaxImportRecords})
	if err != nil {
		ve := &ValidationError{Code: "zone_file_invalid", Message: err.Error()}
		var errs zonefile.Errors
		if errors.As(err, &errs) {
			for _, le := range errs {
				ve.Details = append(ve.Details, LineError{Line: le.Line, Message: le.Message})
			}
		}
		return nil, ve
	}
	if err := CheckSet(current.Name, res.Records); err != nil {
		return nil, err
	}
	soa := res.SOA
	out, err := s.Mutate(ctx, zoneID, func(tx pgx.Tx, z *Zone) (string, any, any, RebuildOptions, error) {
		if err := writable(z); err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		if z.Revision != revision {
			return "", nil, nil, RebuildOptions{}, fmt.Errorf("zone revision %d is stale (current %d): %w", revision, z.Revision, store.ErrConflict)
		}
		if err := SetRecords(ctx, tx, z.ID, res.Records); err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		defaultTTL := int64(z.DefaultTTL)
		if res.DefaultTTL > 0 {
			defaultTTL = int64(res.DefaultTTL)
		}
		if soa.Refresh == 0 || soa.Retry == 0 || soa.Expire == 0 {
			return "", nil, nil, RebuildOptions{}, invalid("invalid_soa", "soa refresh, retry and expire must be positive")
		}
		for _, v := range []uint32{soa.Refresh, soa.Retry, soa.Expire, soa.Minttl, soa.Hdr.Ttl} {
			if v > maxTTL {
				return "", nil, nil, RebuildOptions{}, invalid("invalid_soa", fmt.Sprintf("soa timer %d exceeds %d", v, maxTTL))
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE zones SET default_ttl = $2, soa_mname = $3, soa_rname = $4, soa_refresh = $5, soa_retry = $6,
			soa_expire = $7, soa_minimum = $8, soa_ttl = $9 WHERE id = $1`, z.ID, defaultTTL, strings.ToLower(soa.Ns), strings.ToLower(soa.Mbox),
			int64(soa.Refresh), int64(soa.Retry), int64(soa.Expire), int64(soa.Minttl), int64(soa.Hdr.Ttl)); err != nil {
			return "", nil, nil, RebuildOptions{}, err
		}
		serial := soa.Serial
		after := map[string]any{"records": len(res.Records), "soa_serial": serial}
		return "importZoneFile", map[string]any{"revision": z.Revision, "serial": z.Serial}, after, RebuildOptions{Serial: &serial}, nil
	}, actor)
	if err != nil {
		return nil, err
	}
	return &ImportResult{Zone: out, RecordsImported: len(res.Records)}, nil
}

// Export writes zone zoneID as a BIND master file.
// debt: loads every record into memory; revisit when exports of multi-million-record zones are needed.
func (s *Service) Export(ctx context.Context, zoneID uuid.UUID, w io.Writer) error {
	z, err := s.GetZone(ctx, zoneID)
	if err != nil {
		return err
	}
	rrs, err := loadRecordRRs(ctx, s.Store.Pool, zoneID)
	if err != nil {
		return store.MapError(err)
	}
	return zonefile.Export(w, z.Name, z.DefaultTTL, soaRR(z), rrs)
}
