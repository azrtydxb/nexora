package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

var tsigAlgorithms = map[string]controlv1.TsigAlgorithm{
	"hmac-sha256": controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA256,
	"hmac-sha384": controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA384,
	"hmac-sha512": controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA512,
}

// TSIGKeys loads the hosted-zone TSIG keys for delivery in KeyMaterial. The secrets are unsealed
// per load and never logged, persisted or put in a snapshot.
type TSIGKeys struct {
	st  *store.Store
	box *secrets.Box
}

// NewTSIGKeys creates the key loader.
func NewTSIGKeys(st *store.Store, box *secrets.Box) *TSIGKeys {
	return &TSIGKeys{st: st, box: box}
}

// Load returns the complete key set and its digest ("" when there are no keys).
func (k *TSIGKeys) Load(ctx context.Context) (*controlv1.KeyMaterial, string, error) {
	rows, err := k.st.Pool.Query(ctx, `SELECT id, name, algorithm, secret_envelope FROM tsig_keys ORDER BY name`)
	if err != nil {
		return nil, "", store.MapError(err)
	}
	defer rows.Close()
	km := &controlv1.KeyMaterial{}
	for rows.Next() {
		var id uuid.UUID
		var name, alg string
		var envelope []byte
		if err := rows.Scan(&id, &name, &alg, &envelope); err != nil {
			clearKeyMaterial(km)
			return nil, "", store.MapError(err)
		}
		secret, err := k.box.Unseal(secrets.TSIGPurpose(id, name, alg), envelope)
		if err != nil {
			clearKeyMaterial(km)
			return nil, "", fmt.Errorf("tsig key %s: %w", name, err)
		}
		km.TsigKeys = append(km.TsigKeys, &controlv1.TsigSecret{Name: name, Algorithm: tsigAlgorithms[alg], Secret: secret})
	}
	if err := rows.Err(); err != nil {
		clearKeyMaterial(km)
		return nil, "", store.MapError(err)
	}
	if len(km.TsigKeys) == 0 {
		return km, "", nil
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(km)
	if err != nil {
		clearKeyMaterial(km)
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	clear(raw)
	return km, hex.EncodeToString(sum[:]), nil
}

// ZoneKeyNames returns the names of the keys some zone of any engine group uses (transfer, NOTIFY
// target, update or primary key).
func (k *TSIGKeys) ZoneKeyNames(ctx context.Context) (map[string]bool, error) {
	rows, err := k.st.Pool.Query(ctx, `SELECT k.name FROM tsig_keys k WHERE EXISTS (SELECT 1 FROM zones z
		WHERE z.transfer_tsig_key_id = k.id OR k.id = ANY(z.update_tsig_key_ids)
		OR EXISTS (SELECT 1 FROM jsonb_array_elements(z.notify_targets || z.primaries) e WHERE e->>'tsig_key_id' = k.id::text))`)
	if err != nil {
		return nil, store.MapError(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, store.MapError(err)
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, nil
}

// clearKeyMaterial zeroes the secrets of km (nil-safe).
func clearKeyMaterial(km *controlv1.KeyMaterial) {
	for _, k := range km.GetTsigKeys() {
		clear(k.Secret)
	}
}
