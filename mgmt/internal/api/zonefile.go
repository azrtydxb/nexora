package api

import (
	"context"
	"io"
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
	filename := strings.NewReplacer(`"`, "_", `\`, "_").Replace(z.Name) + "zone"
	// The body streams: no Content-Length. A failure after the status line truncates the body and
	// the response error handler logs it; a client that goes away closes the pipe and the query.
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(h.d.Zones.ExportTo(ctx, req.ZoneId, pw)) }()
	return ExportZoneFile200TextplainCharsetUtf8Response{
		Body:    pr,
		Headers: ExportZoneFile200ResponseHeaders{ContentDisposition: `attachment; filename="` + filename + `"`},
	}, nil
}
