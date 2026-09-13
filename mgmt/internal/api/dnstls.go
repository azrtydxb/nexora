package api

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// GetDnsTlsStatus reports the DNS serving certificate this instance loaded and what each live engine
// accepted. The response carries certificate metadata only, never PEM or key material.
func (h *handlers) GetDnsTlsStatus(ctx context.Context, _ GetDnsTlsStatusRequestObject) (GetDnsTlsStatusResponseObject, error) {
	states, err := store.ListEngineTLSState(ctx, h.d.Store.Pool)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Store.Pool.Query(ctx, "select id, node_name from engines where deleted_at is null")
	if err != nil {
		return nil, store.MapError(err)
	}
	names := map[uuid.UUID]string{}
	var id uuid.UUID
	var name string
	if _, err := pgx.ForEachRow(rows, []any{&id, &name}, func() error { names[id] = name; return nil }); err != nil {
		return nil, store.MapError(err)
	}
	states = slices.DeleteFunc(states, func(s store.EngineTLSState) bool { _, live := names[s.EngineID]; return !live })

	var out DnsTlsStatus
	out.Engines = makeOf(out.Engines, len(states)) // non-nil: engines is a required array
	for i, s := range states {
		e := &out.Engines[i]
		e.EngineId, e.NodeName, e.FingerprintSha256 = s.EngineID, names[s.EngineID], s.Fingerprint
		e.Applied, e.Error, e.UpdatedAt = s.Applied, s.Error, s.UpdatedAt
	}
	if h.d.DNSTLS == nil {
		return GetDnsTlsStatus200JSONResponse(out), nil
	}
	if m := h.d.DNSTLS.Current(); m != nil {
		out.Configured = true
		out.Certificate = newOf(out.Certificate)
		c := out.Certificate
		c.Subject, c.DnsNames, c.IpAddresses = m.Subject, m.DNSNames, m.IPAddresses
		c.NotBefore, c.NotAfter, c.FingerprintSha256 = m.NotBefore, m.NotAfter, m.FingerprintSHA256
	}
	return GetDnsTlsStatus200JSONResponse(out), nil
}

// newOf and makeOf allocate the anonymous struct types the generator emits for inline schemas.
func newOf[T any](*T) *T { return new(T) }

func makeOf[T any](_ []T, n int) []T { return make([]T, n) }
