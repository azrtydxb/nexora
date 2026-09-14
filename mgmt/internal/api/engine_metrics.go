package api

import (
	"context"
	"slices"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/fleet"
)

var metricsWindows = map[GetEngineMetricsParamsWindow]time.Duration{"5m": 5 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour}

// GetEngineMetrics returns the engine modal's series: rates, latency, resources, connections and
// per-upstream series over the window, with restarts and the newest filter index statistics.
func (h *handlers) GetEngineMetrics(ctx context.Context, req GetEngineMetricsRequestObject) (GetEngineMetricsResponseObject, error) {
	window := GetEngineMetricsParamsWindowN1h
	if req.Params.Window != nil {
		window = *req.Params.Window
	}
	d, ok := metricsWindows[window]
	if !ok {
		return nil, invalid("window must be 5m, 1h or 24h")
	}
	if _, err := getEngine(ctx, h.d.Store.Pool, req.Id); err != nil {
		return nil, err
	}
	m, err := fleet.EngineMetrics(ctx, h.d.Store.Pool, req.Id, d)
	if err != nil {
		return nil, err
	}
	out := EngineMetrics{Window: EngineMetricsWindow(window), Restarts: m.Restarts, StartedAt: m.StartedAt}
	out.Samples = makeOf(out.Samples, len(m.Points))
	for i, p := range m.Points {
		s := &out.Samples[i]
		s.At, s.Qps, s.P50Ms, s.P99Ms = p.At, float32(p.QPS), float32(p.P50Ms), float32(p.P99Ms)
		s.CacheHitRatio, s.ServfailRatio, s.NxdomainRatio, s.RefusedRatio = float32(p.CacheHitRatio), float32(p.ServfailRatio), float32(p.NXDomainRatio), float32(p.RefusedRatio)
		s.BlockedQps, s.CpuCores, s.ResidentBytes, s.MemoryLimitBytes = float32(p.BlockedQPS), float32(p.CPUCores), int64(p.ResidentBytes), int64(p.MemoryLimitBytes)
		s.Connections = make(map[string]float32, len(p.Connections))
		for k, v := range p.Connections {
			s.Connections[k] = float32(v)
		}
	}
	names := make([]string, 0, len(m.Upstreams))
	for name := range m.Upstreams {
		names = append(names, name)
	}
	slices.Sort(names)
	out.Upstreams = makeOf(out.Upstreams, len(names))
	for i, name := range names {
		u := &out.Upstreams[i]
		u.Name = name
		u.Samples = makeOf(u.Samples, len(m.Upstreams[name]))
		for j, p := range m.Upstreams[name] {
			u.Samples[j].At, u.Samples[j].RttMs = p.At, float32(p.RTTMs)
			u.Samples[j].FailuresPerSecond, u.Samples[j].RaceWinsPerSecond = float32(p.FailuresPerSecond), float32(p.RaceWinsPerSecond)
		}
	}
	fi, at, err := fleet.LatestFilterIndex(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if fi != nil {
		f := newOf(out.FilterIndex)
		f.At, f.Entries, f.Bytes, f.MaxBytes, f.Cpu = at, int64(fi.Entries), int64(fi.Bytes), int64(fi.MaxBytes), fi.Cpu
		f.BuildSeconds, f.DecisionNsBlocked, f.DecisionNsClean = float32(fi.BuildSeconds), float32(fi.DecisionNsBlocked), float32(fi.DecisionNsClean)
		out.FilterIndex = f
	}
	return GetEngineMetrics200JSONResponse(out), nil
}
