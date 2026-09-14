package api

import (
	"context"
	"errors"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
)

// GetDashboardSeries returns the fleet time series over the range.
func (h *handlers) GetDashboardSeries(ctx context.Context, req GetDashboardSeriesRequestObject) (GetDashboardSeriesResponseObject, error) {
	r := stats.Range(req.Params.Range)
	if _, ok := r.Span(); !ok {
		return nil, invalid("%s", stats.ErrInvalidRange)
	}
	d, err := stats.DashboardSeries(ctx, h.d.Store.Pool, r, time.Now())
	if err != nil {
		return nil, err
	}
	out := DashboardSeries{Range: DashboardSeriesRange(r), StepSeconds: d.StepSeconds}
	out.Points = makeOf(out.Points, len(d.Points))
	for i, p := range d.Points {
		o := &out.Points[i]
		o.At, o.Qps, o.BlockedQps, o.RewrittenQps = p.At, float32(p.QPS), float32(p.BlockedQPS), float32(p.RewrittenQPS)
		o.QpsByTransport, o.QpsByRcode = float32Map(p.QPSByTransport), float32Map(p.QPSByRcode)
		o.BlockedByCategory, o.AnswersByRoute = float32Map(p.BlockedByCategory), float32Map(p.AnswersByRoute)
		o.P50Ms, o.P95Ms, o.P99Ms = float32(p.P50Ms), float32(p.P95Ms), float32(p.P99Ms)
		o.MissP50Ms, o.MissP95Ms, o.MissP99Ms = float32(p.MissP50Ms), float32(p.MissP95Ms), float32(p.MissP99Ms)
		o.CacheHitRatio, o.CacheMissRatio, o.CacheStaleRatio = float32(p.CacheHitRatio), float32(p.CacheMissRatio), float32(p.CacheStaleRatio)
		o.RecursionUpstreamQps, o.RecursionTimeoutsQps, o.LameMarked = float32(p.RecursionUpstreamQPS), float32(p.RecursionTimeoutsQPS), float32(p.LameMarked)
		o.ResolutionFailuresQps = float32(p.ResolutionFailuresQPS)
		o.DnssecSecureQps, o.DnssecInsecureQps, o.DnssecBogusQps = float32(p.DNSSECSecureQPS), float32(p.DNSSECInsecureQPS), float32(p.DNSSECBogusQPS)
	}
	out.Engines = makeOf(out.Engines, len(d.Engines))
	for i, e := range d.Engines {
		o := &out.Engines[i]
		o.EngineId, o.NodeName = e.EngineID, e.NodeName
		o.CacheEntries, o.CacheBytes, o.FilterIndexBytes = int64(e.CacheEntries), int64(e.CacheBytes), int64(e.FilterIndexBytes)
	}
	return GetDashboardSeries200JSONResponse(out), nil
}

func float32Map(m map[string]float64) map[string]float32 {
	out := make(map[string]float32, len(m))
	for k, v := range m {
		out[k] = float32(v)
	}
	return out
}

// GetDashboardTop returns the top domains, blocked domains, clients and categories of the range from
// the query log backend; available is false, with empty lists, when the backend cannot aggregate.
func (h *handlers) GetDashboardTop(ctx context.Context, req GetDashboardTopRequestObject) (GetDashboardTopResponseObject, error) {
	r := stats.Range(req.Params.Range)
	span, ok := r.Span()
	if !ok {
		return nil, invalid("%s", stats.ErrInvalidRange)
	}
	limit, err := limitParam(req.Params.Limit, 10, 50)
	if err != nil {
		return nil, err
	}
	out := DashboardTop{Range: DashboardTopRange(r)}
	out.Domains, out.BlockedDomains = makeOf(out.Domains, 0), makeOf(out.BlockedDomains, 0)
	out.Clients, out.Categories = makeOf(out.Clients, 0), makeOf(out.Categories, 0)
	topper, ok := h.d.QueryLog.(querylog.Topper)
	if !ok {
		return GetDashboardTop200JSONResponse(out), nil
	}
	to := time.Now()
	lists := []struct {
		field   querylog.TopField
		filters []string
		dst     *[]struct {
			Count int64  `json:"count"`
			Key   string `json:"key"`
		}
	}{
		{querylog.TopName, nil, &out.Domains},
		{querylog.TopName, []string{"blocked"}, &out.BlockedDomains},
		{querylog.TopClient, nil, &out.Clients},
		{querylog.TopCategory, nil, &out.Categories},
	}
	for _, l := range lists {
		entries, err := topper.Top(ctx, querylog.TopQuery{From: to.Add(-span), To: to, Field: l.field, Filters: l.filters, Limit: limit})
		if errors.Is(err, querylog.ErrBackendUnavailable) {
			empty := DashboardTop{Range: out.Range}
			empty.Domains, empty.BlockedDomains = makeOf(empty.Domains, 0), makeOf(empty.BlockedDomains, 0)
			empty.Clients, empty.Categories = makeOf(empty.Clients, 0), makeOf(empty.Categories, 0)
			return GetDashboardTop200JSONResponse(empty), nil
		}
		if err != nil {
			return nil, err
		}
		*l.dst = makeOf(*l.dst, len(entries))
		for i, e := range entries {
			(*l.dst)[i].Key, (*l.dst)[i].Count = e.Key, e.Count
		}
	}
	out.Available = true
	return GetDashboardTop200JSONResponse(out), nil
}

// GetDashboardHealth returns the engine and engine group tables and the fleet alerts.
func (h *handlers) GetDashboardHealth(ctx context.Context, _ GetDashboardHealthRequestObject) (GetDashboardHealthResponseObject, error) {
	d, err := stats.DashboardHealth(ctx, h.d.Store.Pool, time.Now())
	if err != nil {
		return nil, err
	}
	var out DashboardHealth
	out.Engines = makeOf(out.Engines, len(d.Engines))
	for i, e := range d.Engines {
		o := &out.Engines[i]
		o.Id, o.NodeName, o.EngineGroupName, o.Status = e.ID, e.NodeName, e.EngineGroupName, e.Status
		o.Qps, o.P99Ms, o.CacheHitRatio = float32(e.QPS), float32(e.P99Ms), float32(e.CacheHitRatio)
		o.FilterIndexBytes, o.AppliedVersion, o.TargetVersion = int64(e.FilterIndexBytes), int64(e.AppliedVersion), int64(e.TargetVersion)
	}
	out.Groups = makeOf(out.Groups, len(d.Groups))
	for i, g := range d.Groups {
		out.Groups[i].Id, out.Groups[i].Name, out.Groups[i].Engines, out.Groups[i].Connected = g.ID, g.Name, g.Engines, g.Connected
	}
	out.Alerts = makeOf(out.Alerts, len(d.Alerts))
	for i, a := range d.Alerts {
		o := &out.Alerts[i]
		o.Kind, o.Severity, o.Subject, o.Message = DashboardHealthAlertsKind(a.Kind), DashboardHealthAlertsSeverity(a.Severity), a.Subject, a.Message
	}
	return GetDashboardHealth200JSONResponse(out), nil
}
