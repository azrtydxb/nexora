package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/rpz"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

var rpzTsigAlgorithms = map[string]controlv1.TsigAlgorithm{
	"hmac-sha256": controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA256,
	"hmac-sha512": controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA512,
}

// RPZTsig loads the TSIG secrets of RPZ transfer zones for delivery on the control stream. The
// secrets are unsealed per load and never logged or stored in a snapshot.
type RPZTsig struct {
	st  *store.Store
	box *secrets.Box
}

// NewRPZTsig creates the key loader.
func NewRPZTsig(st *store.Store, box *secrets.Box) *RPZTsig {
	return &RPZTsig{st: st, box: box}
}

// Load returns the complete key set and its digest ("" when no zone has a key).
func (r *RPZTsig) Load(ctx context.Context) (*controlv1.RpzTsigKeys, string, error) {
	rows, err := r.st.Pool.Query(ctx, `select id, tsig_key_name, tsig_algorithm, tsig_secret_envelope from rpz_zones
		where tsig_secret_envelope is not null order by id`)
	if err != nil {
		return nil, "", store.MapError(err)
	}
	defer rows.Close()
	keys := &controlv1.RpzTsigKeys{}
	for rows.Next() {
		var id uuid.UUID
		var name, alg string
		var envelope []byte
		if err := rows.Scan(&id, &name, &alg, &envelope); err != nil {
			return nil, "", store.MapError(err)
		}
		secret, err := r.box.Unseal(rpz.TsigPurpose(id), envelope)
		if err != nil {
			return nil, "", fmt.Errorf("rpz zone %s: %w", id, err)
		}
		keys.Keys = append(keys.Keys, &controlv1.RpzTsigKey{ZoneId: id.String(), KeyName: name, Algorithm: rpzTsigAlgorithms[alg], Secret: secret})
	}
	if err := rows.Err(); err != nil {
		return nil, "", store.MapError(err)
	}
	if len(keys.Keys) == 0 {
		return keys, "", nil
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(keys)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	clear(raw)
	return keys, hex.EncodeToString(sum[:]), nil
}
