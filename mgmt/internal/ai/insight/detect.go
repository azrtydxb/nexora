// Package insight is the dashboard insight agent (M11 S-5): deterministic detectors over the dashboard
// series, the per-engine samples and the fleet health produce candidates; the model only correlates and
// explains them.
package insight

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Kind is the finding kind of every insight.
const Kind = "insight"

// currentWindow is the recent window compared against the previous 24 h.
const currentWindow = 15 * time.Minute

// Thresholds of the detectors (M11 S-5).
const (
	servfailFactor, servfailMin, servfailCritical = 3.0, 0.02, 0.10
	latencyFactor, latencyMinMs                   = 2.0, 50.0
	qpsFactor, blockFactor, rttFactor             = 3.0, 3.0, 2.0
	certificateCritical                           = 3 * 24 * time.Hour
)

// Score is the health score: 10 is healthy, with 3 points deducted per critical
// and 1 per warning insight, floored at 0. The input contains only open insights.
func Score(open []finding.Finding) int {
	score := 0
	for _, f := range open {
		switch f.Severity {
		case "critical":
			score += 3
		case "warning":
			score++
		}
	}
	return max(0, 10-score)
}

// Detect returns the insight candidates at now: the last 15 minutes against the previous 24 hours for
// the fleet series and each engine's samples, plus the fleet health alerts.
func Detect(ctx context.Context, q store.PolicyQuerier, now time.Time) ([]finding.Candidate, error) {
	boundary := now.Add(-currentWindow)
	var out []finding.Candidate

	recent, err := stats.DashboardSeries(ctx, q, "15m", now)
	if err != nil {
		return nil, err
	}
	day, err := stats.DashboardSeries(ctx, q, "24h", now)
	if err != nil {
		return nil, err
	}
	step := time.Duration(day.StepSeconds) * time.Second
	var base, cur mean
	var baseBlocked, curBlocked mean
	for _, p := range day.Points {
		if !p.At.Add(step).After(boundary) {
			base.add(p.QPS, 1)
			baseBlocked.add(p.BlockedQPS, 1)
		}
	}
	for _, p := range recent.Points {
		cur.add(p.QPS, 1)
		curBlocked.add(p.BlockedQPS, 1)
	}
	if spike(cur, base, qpsFactor) {
		out = append(out, candidate("qps_spike", "fleet", "warning", "Fleet query rate spike",
			fmt.Sprintf("Fleet queries are %.1f qps over the last 15 minutes against a 24 h baseline of %.1f qps.", cur.value(), base.value()),
			map[string]any{"current_qps": cur.value(), "baseline_qps": base.value()}))
	}
	if spike(curBlocked, baseBlocked, blockFactor) {
		out = append(out, candidate("block_spike", "fleet", "warning", "Fleet blocked query spike",
			fmt.Sprintf("Blocked queries are %.1f qps over the last 15 minutes against a 24 h baseline of %.1f qps.", curBlocked.value(), baseBlocked.value()),
			map[string]any{"current_blocked_qps": curBlocked.value(), "baseline_blocked_qps": baseBlocked.value()}))
	}

	health, err := stats.DashboardHealth(ctx, q, now)
	if err != nil {
		return nil, err
	}
	rtt := map[string]*[2]mean{} // upstream name -> {baseline, current}
	for _, e := range health.Engines {
		// debt: decodes every sample of every engine over 24 h each run (8,640 per engine); revisit with a
		// 1-minute rollup when a fleet above 50 engines makes a run exceed 5 s.
		m, err := fleet.EngineMetrics(ctx, q, e.ID, 24*time.Hour)
		if err != nil {
			return nil, err
		}
		out = append(out, engineCandidates(e.NodeName, m, boundary)...)
		for name, points := range m.Upstreams {
			if rtt[name] == nil {
				rtt[name] = &[2]mean{}
			}
			for i := 1; i < len(points); i++ {
				gap := points[i].At.Sub(points[i-1].At).Seconds()
				switch {
				case !points[i].At.After(boundary):
					rtt[name][0].add(points[i].RTTMs, gap)
				case !points[i-1].At.Before(boundary):
					rtt[name][1].add(points[i].RTTMs, gap)
				}
			}
		}
	}

	down := map[string]bool{}
	for _, a := range health.Alerts {
		switch a.Kind {
		case stats.AlertUpstreamDown:
			down[a.Subject] = true
			out = append(out, candidate("upstream_degraded", a.Subject, "critical", "Upstream "+a.Subject+" is down", "Upstream "+a.Subject+" is "+a.Message+".",
				map[string]any{"upstream": a.Subject, "up": false}))
		case stats.AlertEngineDisconnected:
			out = append(out, candidate("engine_disconnected", a.Subject, "critical", "Engine "+a.Subject+" is disconnected", a.Message+".",
				map[string]any{"engine": a.Subject}))
		case stats.AlertExportDropped:
			out = append(out, candidate("export_dropped", a.Subject, "warning", "Engine "+a.Subject+" drops telemetry", a.Message+".",
				map[string]any{"engine": a.Subject}))
		case stats.AlertCertificateExpiring:
			out = append(out, certificateCandidate(a, now))
		}
	}
	names := make([]string, 0, len(rtt))
	for name := range rtt {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b, c := rtt[name][0], rtt[name][1]
		if down[name] || !spike(c, b, rttFactor) {
			continue
		}
		out = append(out, candidate("upstream_degraded", name, "warning", "Upstream "+name+" is slow",
			fmt.Sprintf("Upstream %s answers in %.0f ms over the last 15 minutes against a 24 h mean of %.0f ms.", name, c.value(), b.value()),
			map[string]any{"upstream": name, "up": true, "current_rtt_ms": c.value(), "baseline_rtt_ms": b.value()}))
	}
	return out, nil
}

// engineCandidates compares one engine's points after boundary with those before it. A point's
// interval runs from the previous point, and a point whose interval straddles boundary is ignored.
func engineCandidates(node string, m fleet.EngineMetricsData, boundary time.Time) []finding.Candidate {
	var base, cur struct{ qps, servfail, p99 mean }
	for i := 1; i < len(m.Points); i++ {
		p, prev := m.Points[i], m.Points[i-1]
		gap := p.At.Sub(prev.At).Seconds()
		w := &base
		switch {
		case !p.At.After(boundary):
		case !prev.At.Before(boundary):
			w = &cur
		default:
			continue
		}
		w.qps.add(p.QPS, gap)
		w.servfail.add(p.ServfailRatio, p.QPS*gap)
		w.p99.add(p.P99Ms, p.QPS*gap)
	}
	var out []finding.Candidate
	detail := func(kv ...any) map[string]any {
		d := map[string]any{"engine": node}
		for i := 0; i+1 < len(kv); i += 2 {
			d[kv[i].(string)] = kv[i+1]
		}
		return d
	}
	if base.servfail.n > 0 && cur.servfail.n > 0 {
		r, b := cur.servfail.value(), base.servfail.value()
		if r >= servfailFactor*b && r >= servfailMin {
			severity := "warning"
			if r >= servfailCritical {
				severity = "critical"
			}
			out = append(out, candidate("servfail_spike", node, severity, "SERVFAIL spike on "+node,
				fmt.Sprintf("%.1f%% of the queries on %s failed with SERVFAIL over the last 15 minutes against %.2f%% over the previous 24 h.", 100*r, node, 100*b),
				detail("current_ratio", r, "baseline_ratio", b)))
		}
	}
	if base.p99.n > 0 && cur.p99.n > 0 {
		l, b := cur.p99.value(), base.p99.value()
		if l >= latencyFactor*b && l >= latencyMinMs {
			out = append(out, candidate("latency_spike", node, "warning", "Latency spike on "+node,
				fmt.Sprintf("p99 latency on %s is %.0f ms over the last 15 minutes against %.0f ms over the previous 24 h.", node, l, b),
				detail("current_p99_ms", l, "baseline_p99_ms", b)))
		}
	}
	if spike(cur.qps, base.qps, qpsFactor) {
		out = append(out, candidate("qps_spike", node, "warning", "Query rate spike on "+node,
			fmt.Sprintf("%s serves %.1f qps over the last 15 minutes against a 24 h baseline of %.1f qps.", node, cur.qps.value(), base.qps.value()),
			detail("current_qps", cur.qps.value(), "baseline_qps", base.qps.value())))
	}
	return out
}

// certificateCandidate grades an expiring certificate critical within 3 days; the expiry is read from
// the health alert message ("DNS serving certificate expires <RFC 3339>"), falling back to its severity.
func certificateCandidate(a stats.Alert, now time.Time) finding.Candidate {
	severity := a.Severity
	d := map[string]any{"engine": a.Subject}
	if s, ok := strings.CutPrefix(a.Message, "DNS serving certificate expires "); ok {
		if notAfter, err := time.Parse(time.RFC3339, s); err == nil {
			severity = "warning"
			if notAfter.Sub(now) < certificateCritical {
				severity = "critical"
			}
			d["not_after"] = notAfter
		}
	}
	return candidate("certificate_expiring", a.Subject, severity, "Certificate of "+a.Subject+" expires soon", a.Message+".", d)
}

// spike: current ≥ factor × a positive baseline.
func spike(cur, base mean, factor float64) bool {
	return cur.n > 0 && base.n > 0 && base.value() > 0 && cur.value() >= factor*base.value()
}

func candidate(typ, subject, severity, title, description string, detail map[string]any) finding.Candidate {
	return finding.Candidate{ID: typ + ":" + subject, Kind: Kind, Type: typ, Severity: severity, Title: title,
		Description: description, Detail: detail}
}

// mean is a weighted mean; n counts the added values.
type mean struct {
	sum, weight float64
	n           int
}

func (m *mean) add(v, w float64) {
	m.n++
	if w > 0 {
		m.sum += v * w
		m.weight += w
	}
}

func (m mean) value() float64 {
	if m.weight == 0 {
		return 0
	}
	return m.sum / m.weight
}
