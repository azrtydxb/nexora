package api

import (
	"context"

	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
)

func tsigKeyOut(k tsigkey.Key) TsigKey {
	return TsigKey{Id: k.ID, Name: k.Name, Algorithm: TsigKeyAlgorithm(k.Algorithm), Revision: k.Revision, CreatedAt: k.CreatedAt}
}

func (h *handlers) ListTsigKeys(ctx context.Context, _ ListTsigKeysRequestObject) (ListTsigKeysResponseObject, error) {
	keys, err := h.d.TSIGKeys.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(ListTsigKeys200JSONResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, tsigKeyOut(k))
	}
	return out, nil
}

func (h *handlers) CreateTsigKey(ctx context.Context, req CreateTsigKeyRequestObject) (CreateTsigKeyResponseObject, error) {
	b := req.Body
	secret := ""
	if b.Secret != nil {
		secret = *b.Secret
	}
	c, err := h.d.TSIGKeys.Create(ctx, PrincipalFrom(ctx).Actor(), b.Name, string(b.Algorithm), secret)
	if err != nil {
		return nil, err
	}
	k := tsigKeyOut(c.Key)
	return CreateTsigKey201JSONResponse{Id: k.Id, Name: k.Name, Algorithm: k.Algorithm, Revision: k.Revision, CreatedAt: k.CreatedAt, Secret: c.Secret}, nil
}

func (h *handlers) DeleteTsigKey(ctx context.Context, req DeleteTsigKeyRequestObject) (DeleteTsigKeyResponseObject, error) {
	if err := h.d.TSIGKeys.Delete(ctx, PrincipalFrom(ctx).Actor(), req.KeyId, req.Params.Revision); err != nil {
		return nil, err
	}
	return DeleteTsigKey204Response{}, nil
}
