package api

import (
	"bytes"
	"context"
	"strings"
)

func (h *handlers) ImportZoneFile(ctx context.Context, req ImportZoneFileRequestObject) (ImportZoneFileResponseObject, error) {
	res, err := h.d.Zones.Import(ctx, PrincipalFrom(ctx).Actor(), req.ZoneId, req.Body.Revision, req.Body.Content)
	if err != nil {
		return nil, err
	}
	return ImportZoneFile200JSONResponse{Zone: zoneOut(res.Zone), RecordsImported: res.RecordsImported}, nil
}

func (h *handlers) ExportZoneFile(ctx context.Context, req ExportZoneFileRequestObject) (ExportZoneFileResponseObject, error) {
	z, err := h.d.Zones.GetZone(ctx, req.ZoneId)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := h.d.Zones.Export(ctx, req.ZoneId, &buf); err != nil {
		return nil, err
	}
	filename := strings.NewReplacer(`"`, "_", `\`, "_").Replace(z.Name) + "zone"
	return ExportZoneFile200TextplainCharsetUtf8Response{
		Body:          &buf,
		ContentLength: int64(buf.Len()),
		Headers:       ExportZoneFile200ResponseHeaders{ContentDisposition: `attachment; filename="` + filename + `"`},
	}, nil
}
