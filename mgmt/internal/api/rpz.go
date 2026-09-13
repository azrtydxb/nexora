package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dnssecconf"
	"github.com/piwi3910/nexora/mgmt/internal/rpz"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func rpzZoneOut(z store.RPZZone, status []store.RPZEngineStatus) RpzZone {
	out := RpzZone{
		Id: z.ID, Name: z.Name, Position: int(z.Position), SourceType: RpzZoneSourceType(z.SourceType),
		Primary: z.PrimaryAddress, TsigKeyName: z.TSIGKeyName, TsigAlgorithm: z.TSIGAlgorithm, TsigSecretSet: z.TSIGSecretEnvelope != nil,
		MinRefreshSeconds: int(z.MinRefreshSeconds), PolicyOverride: z.PolicyOverride, Revision: z.Revision,
	}
	if z.FileRecords != nil {
		n := int(*z.FileRecords)
		out.FileRecords = &n
	}
	out.Status = makeOf(out.Status, len(status))
	for i, s := range status {
		o := &out.Status[i]
		o.EngineId, o.EngineName, o.Serial, o.Records, o.Skipped, o.Hits = s.EngineID, s.EngineName, s.Serial, s.Records, s.Skipped, s.Hits
		o.LastSuccess, o.LastError, o.Stale = s.LastSuccessAt, s.LastError, s.Stale
	}
	return out
}

// rpzFields are the validated source and TSIG fields shared by create and update.
type rpzFields struct {
	primary, keyName, algorithm *string
	secret                      []byte // decoded; nil when not provided
}

// validateRPZFields checks the source-specific fields of a zone of sourceType.
func validateRPZFields(sourceType string, primary, keyName, algorithm, secret *string, minRefresh int, override string) (rpzFields, error) {
	var f rpzFields
	if !RpzZoneInputPolicyOverride(override).Valid() {
		return f, invalid("policy_override %q is not supported", override)
	}
	if minRefresh < 1 || minRefresh > 86400 {
		return f, invalid("min_refresh_seconds must be 1..86400")
	}
	if sourceType == "file" {
		if primary != nil || keyName != nil || algorithm != nil || secret != nil {
			return f, invalid("file zones take no primary or TSIG fields")
		}
		return f, nil
	}
	if primary == nil || !dnssecconf.IsIPPort(*primary) {
		return f, invalid("primary must be ip:port for transfer zones")
	}
	f.primary = primary
	if algorithm == nil {
		if keyName != nil || secret != nil {
			return f, invalid("tsig_key_name and tsig_secret require tsig_algorithm")
		}
		return f, nil
	}
	if !RpzZoneInputTsigAlgorithm(*algorithm).Valid() || *algorithm == string(RpzZoneInputTsigAlgorithmLessThannil) {
		return f, invalid("tsig_algorithm must be hmac-sha256 or hmac-sha512")
	}
	if keyName == nil {
		return f, invalid("tsig_algorithm requires tsig_key_name")
	}
	name, err := dnssecconf.ValidateDomain(*keyName)
	if err != nil {
		return f, invalid("tsig_key_name: %s", err.Error())
	}
	f.keyName, f.algorithm = &name, algorithm
	if secret != nil {
		raw, err := base64.StdEncoding.DecodeString(*secret)
		if err != nil || len(raw) < 16 || len(raw) > 64 {
			return f, invalid("tsig_secret must be base64 of 16..64 bytes")
		}
		f.secret = raw
	}
	return f, nil
}

// seal seals a provided TSIG secret for zone id before any transaction starts, so an unconfigured
// key store refuses the request without writing anything.
func (h *handlers) seal(id uuid.UUID, f rpzFields) ([]byte, error) {
	if f.secret == nil {
		return nil, nil
	}
	defer clear(f.secret)
	return h.d.Secrets.Seal(rpz.TsigPurpose(id), f.secret)
}

func (h *handlers) ListRpzZones(ctx context.Context, _ ListRpzZonesRequestObject) (ListRpzZonesResponseObject, error) {
	zones, err := store.ListRPZZones(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	status, err := store.ListRPZEngineStatus(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	out := make(ListRpzZones200JSONResponse, 0, len(zones))
	for _, z := range zones {
		out = append(out, rpzZoneOut(z, status[z.ID]))
	}
	return out, nil
}

func (h *handlers) GetRpzZone(ctx context.Context, req GetRpzZoneRequestObject) (GetRpzZoneResponseObject, error) {
	z, err := store.GetRPZZone(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	status, err := store.ListRPZEngineStatus(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	return GetRpzZone200JSONResponse(rpzZoneOut(z, status[z.ID])), nil
}

func (h *handlers) CreateRpzZone(ctx context.Context, req CreateRpzZoneRequestObject) (CreateRpzZoneResponseObject, error) {
	b := req.Body
	if !b.SourceType.Valid() {
		return nil, invalid("source_type must be file or transfer")
	}
	name, err := dnssecconf.ValidateDomain(b.Name)
	if err != nil {
		return nil, invalid("name: %s", err.Error())
	}
	f, err := validateRPZFields(string(b.SourceType), b.Primary, b.TsigKeyName, (*string)(b.TsigAlgorithm), b.TsigSecret, b.MinRefreshSeconds, string(b.PolicyOverride))
	if err != nil {
		return nil, err
	}
	if f.algorithm != nil && f.secret == nil {
		return nil, invalid("tsig_algorithm requires tsig_secret")
	}
	id := uuid.New()
	envelope, err := h.seal(id, f)
	if err != nil {
		return nil, err
	}
	var after RpzZone
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		created, err := store.CreateRPZZone(ctx, tx, store.RPZZone{
			ID: id, Name: name, SourceType: string(b.SourceType), PrimaryAddress: f.primary, TSIGKeyName: f.keyName,
			TSIGAlgorithm: f.algorithm, TSIGSecretEnvelope: envelope, MinRefreshSeconds: int32(b.MinRefreshSeconds),
			PolicyOverride: string(b.PolicyOverride),
		})
		after = rpzZoneOut(created, nil)
		return auth.Change{Action: "createRpzZone", TargetType: "rpz_zone", TargetID: id.String(), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return CreateRpzZone201JSONResponse(after), nil
}

func (h *handlers) UpdateRpzZone(ctx context.Context, req UpdateRpzZoneRequestObject) (UpdateRpzZoneResponseObject, error) {
	b := req.Body
	current, err := store.GetRPZZone(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	f, err := validateRPZFields(current.SourceType, b.Primary, b.TsigKeyName, (*string)(b.TsigAlgorithm), b.TsigSecret, b.MinRefreshSeconds, string(b.PolicyOverride))
	if err != nil {
		return nil, err
	}
	envelope, err := h.seal(req.Id, f)
	if err != nil {
		return nil, err
	}
	var after RpzZone
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetRPZZone(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		if f.algorithm != nil && envelope == nil && before.TSIGSecretEnvelope == nil {
			return auth.Change{}, invalid("tsig_algorithm requires tsig_secret")
		}
		updated, err := store.UpdateRPZZone(ctx, tx, store.RPZZone{
			ID: req.Id, PrimaryAddress: f.primary, TSIGKeyName: f.keyName, TSIGAlgorithm: f.algorithm, TSIGSecretEnvelope: envelope,
			MinRefreshSeconds: int32(b.MinRefreshSeconds), PolicyOverride: string(b.PolicyOverride), Revision: b.Revision,
		})
		after = rpzZoneOut(updated, nil)
		return auth.Change{Action: "updateRpzZone", TargetType: "rpz_zone", TargetID: req.Id.String(), Before: rpzZoneOut(before, nil), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UpdateRpzZone200JSONResponse(after), nil
}

func (h *handlers) DeleteRpzZone(ctx context.Context, req DeleteRpzZoneRequestObject) (DeleteRpzZoneResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetRPZZone(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "deleteRpzZone", TargetType: "rpz_zone", TargetID: req.Id.String(), Before: rpzZoneOut(before, nil)},
			store.DeleteRPZZone(ctx, tx, req.Id, req.Params.Revision)
	})
	if err != nil {
		return nil, err
	}
	return DeleteRpzZone204Response{}, nil
}

func (h *handlers) UploadRpzZoneFile(ctx context.Context, req UploadRpzZoneFileRequestObject) (UploadRpzZoneFileResponseObject, error) {
	b := req.Body
	current, err := store.GetRPZZone(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if current.SourceType != "file" {
		return nil, fmt.Errorf("%w: only file zones accept an uploaded zone file", store.ErrConflict)
	}
	summary, err := rpz.ValidateZone(current.Name, b.Content)
	if err != nil {
		return nil, invalid("%s", err.Error())
	}
	var after RpzZone
	err = h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetRPZZone(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		sha, _, err := store.PutBlob(ctx, tx, rpz.Pack(b.Content))
		if err != nil {
			return auth.Change{}, err
		}
		updated, err := store.SetRPZZoneFile(ctx, tx, req.Id, b.Revision, sha, int32(summary.Records))
		after = rpzZoneOut(updated, nil)
		return auth.Change{Action: "uploadRpzZoneFile", TargetType: "rpz_zone", TargetID: req.Id.String(), Before: rpzZoneOut(before, nil), After: after}, err
	})
	if err != nil {
		return nil, err
	}
	return UploadRpzZoneFile200JSONResponse(after), nil
}

func (h *handlers) RefreshRpzZone(ctx context.Context, req RefreshRpzZoneRequestObject) (RefreshRpzZoneResponseObject, error) {
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		before, err := store.GetRPZZone(ctx, tx, req.Id)
		if err != nil {
			return auth.Change{}, err
		}
		if before.SourceType != "transfer" {
			return auth.Change{}, fmt.Errorf("%w: only transfer zones can be refreshed", store.ErrConflict)
		}
		updated, err := store.BumpRPZRefreshNonce(ctx, tx, req.Id)
		return auth.Change{Action: "refreshRpzZone", TargetType: "rpz_zone", TargetID: req.Id.String(),
			After: map[string]int64{"refresh_nonce": updated.RefreshNonce}}, err
	})
	if err != nil {
		return nil, err
	}
	return RefreshRpzZone202Response{}, nil
}

func (h *handlers) ReorderRpzZones(ctx context.Context, req ReorderRpzZonesRequestObject) (ReorderRpzZonesResponseObject, error) {
	ids := req.Body.Ids
	err := h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error) {
		return auth.Change{Action: "reorderRpzZones", TargetType: "rpz_zone", TargetID: "order", After: ids},
			store.ReorderRPZZones(ctx, tx, ids)
	})
	if errors.Is(err, store.ErrConflict) {
		return nil, invalid("ids must list every RPZ zone exactly once")
	}
	if err != nil {
		return nil, err
	}
	return ReorderRpzZones204Response{}, nil
}
