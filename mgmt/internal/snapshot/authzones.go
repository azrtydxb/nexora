package snapshot

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// authJournalListed is how many journal deltas before the current serial each AuthZone lists
// beyond its image, so engines holding a recent version apply deltas instead of the image.
const authJournalListed = 32

var authZoneKinds = map[string]controlv1.AuthZoneKind{
	"primary":   controlv1.AuthZoneKind_AUTH_ZONE_KIND_PRIMARY,
	"secondary": controlv1.AuthZoneKind_AUTH_ZONE_KIND_SECONDARY,
}

type authZoneRow struct {
	id                    string
	az                    *controlv1.AuthZone
	imageSeq, currentSeq  int64
	notifyRaw, primaryRaw []byte
}

type endpointJSON struct {
	Address   string  `json:"address"`
	TSIGKeyID *string `json:"tsig_key_id"`
}

// AddAuthZones lists every served zone (primaries and loaded secondaries) with its image, the
// contiguous journal deltas, transfer/NOTIFY/update settings and TSIG key names.
func AddAuthZones(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot) error {
	keyNames := map[string]string{}
	rows, err := tx.Query(ctx, "SELECT id::text, name FROM tsig_keys")
	if err != nil {
		return fmt.Errorf("tsig keys: %w", err)
	}
	var id, name string
	if _, err := pgx.ForEachRow(rows, []any{&id, &name}, func() error { keyNames[id] = name; return nil }); err != nil {
		return fmt.Errorf("tsig keys: %w", err)
	}

	rows, err = tx.Query(ctx, `SELECT z.id::text, z.name, z.kind, z.serial, z.image_seq, z.current_seq, i.serial, i.blob_sha256, b.size,
		array(SELECT host(c) || '/' || masklen(c) FROM unnest(z.transfer_allow_cidrs) WITH ORDINALITY AS u(c, n) ORDER BY n),
		coalesce(z.transfer_tsig_key_id::text, ''), z.notify_targets, z.primaries,
		array(SELECT k.name FROM tsig_keys k WHERE k.id = ANY(z.update_tsig_key_ids) ORDER BY k.name), z.expired
		FROM zones z JOIN zone_images i ON i.zone_id = z.id AND i.seq = z.image_seq JOIN blobs b ON b.sha256 = i.blob_sha256
		WHERE z.kind = 'primary' OR z.loaded ORDER BY z.name`)
	if err != nil {
		return fmt.Errorf("auth zones: %w", err)
	}
	var zones []authZoneRow
	for rows.Next() {
		r := authZoneRow{az: &controlv1.AuthZone{Image: &controlv1.BlobRef{}, Transfer: &controlv1.TransferPolicy{}}}
		var kind, transferKey string
		var serial, imageSerial, size int64
		if err := rows.Scan(&r.id, &r.az.Name, &kind, &serial, &r.imageSeq, &r.currentSeq, &imageSerial, &r.az.Image.Sha256, &size,
			&r.az.Transfer.AllowCidrs, &transferKey, &r.notifyRaw, &r.primaryRaw, &r.az.UpdateTsigKeys, &r.az.Expired); err != nil {
			rows.Close()
			return fmt.Errorf("auth zones: %w", err)
		}
		r.az.Kind = authZoneKinds[kind]
		r.az.Serial, r.az.ImageSerial = uint32(serial), uint32(imageSerial)
		r.az.Image.Size, r.az.Image.Name = uint64(size), fmt.Sprintf("%s@%d", r.az.Name, imageSerial)
		r.az.Transfer.TsigKey = keyNames[transferKey]
		zones = append(zones, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("auth zones: %w", err)
	}

	for _, r := range zones {
		az := r.az
		var notify, primaries []endpointJSON
		if err := json.Unmarshal(r.notifyRaw, &notify); err != nil {
			return fmt.Errorf("zone %s notify targets: %w", az.Name, err)
		}
		if err := json.Unmarshal(r.primaryRaw, &primaries); err != nil {
			return fmt.Errorf("zone %s primaries: %w", az.Name, err)
		}
		for _, n := range notify {
			t := &controlv1.NotifyTarget{Address: n.Address}
			if n.TSIGKeyID != nil {
				t.TsigKey = keyNames[*n.TSIGKeyID]
			}
			az.Notify = append(az.Notify, t)
		}
		keyed := false
		for _, p := range primaries {
			az.Primaries = append(az.Primaries, p.Address)
			key := ""
			if p.TSIGKeyID != nil {
				key, keyed = keyNames[*p.TSIGKeyID], true
			}
			az.PrimaryTsigKeys = append(az.PrimaryTsigKeys, key)
		}
		if !keyed {
			az.PrimaryTsigKeys = nil // no primary requires a key: keep the field empty
		}
		rows, err := tx.Query(ctx, `SELECT j.seq, j.from_serial, j.to_serial, j.blob_sha256, b.size
			FROM zone_journal j JOIN blobs b ON b.sha256 = j.blob_sha256
			WHERE j.zone_id = $1 AND j.seq > LEAST($2::bigint, $3::bigint - $4) AND j.seq <= $3 ORDER BY j.seq`,
			r.id, r.imageSeq, r.currentSeq, authJournalListed)
		if err != nil {
			return fmt.Errorf("zone %s journal: %w", az.Name, err)
		}
		var seq, from, to, dsize int64
		var sha string
		_, err = pgx.ForEachRow(rows, []any{&seq, &from, &to, &sha, &dsize}, func() error {
			if seq <= r.imageSeq {
				az.ImageDeltaOffset++
			}
			az.Deltas = append(az.Deltas, &controlv1.ZoneDelta{FromSerial: uint32(from), ToSerial: uint32(to),
				Blob: &controlv1.BlobRef{Sha256: sha, Size: uint64(dsize), Name: fmt.Sprintf("%s@%d", az.Name, to)}})
			return nil
		})
		if err != nil {
			return fmt.Errorf("zone %s journal: %w", az.Name, err)
		}
		snap.AuthZones = append(snap.AuthZones, az)
	}
	return nil
}
