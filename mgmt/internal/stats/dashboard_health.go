package stats

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Alert kinds and severities of the dashboard health section.
const (
	AlertEngineDisconnected       = "engine_disconnected"
	AlertCategoryStale            = "category_stale"
	AlertUpstreamDown             = "upstream_down"
	AlertCertificateExpiring      = "certificate_expiring"
	AlertTrustAnchorRefreshFailed = "trust_anchor_refresh_failed"
	AlertExportDropped            = "export_dropped"

	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// healthWindow is how far back the samples behind the sample-based alerts reach.
const healthWindow = 5 * time.Minute

// Alert is one problem the dashboard shows.
type Alert struct {
	Kind, Severity, Subject, Message string
}

// HealthEngine is one engine's row of the health table.
type HealthEngine struct {
	ID                                uuid.UUID
	NodeName, EngineGroupName, Status string
	QPS, P99Ms, CacheHitRatio         float64
	FilterIndexBytes                  uint64
	AppliedVersion, TargetVersion     uint64
}

// HealthGroup is one engine group with its engine counts.
type HealthGroup struct {
	ID                 uuid.UUID
	Name               string
	Engines, Connected int
}

// HealthData is the dashboard health section.
type HealthData struct {
	Engines []HealthEngine
	Groups  []HealthGroup
	Alerts  []Alert
}

// DashboardHealth returns the engines with the fleet status rules and their last-minute figures, the
// engine groups with counts, and the alerts: disconnected engines, stale enabled filter categories,
// upstreams down, DNS serving certificates expiring within 14 days (critical within 7), trust anchor
// refresh errors and telemetry export drops, the last four from the samples of the last 5 minutes.
func DashboardHealth(ctx context.Context, q store.PolicyQuerier, now time.Time) (HealthData, error) {
	h := HealthData{Engines: []HealthEngine{}, Groups: []HealthGroup{}, Alerts: []Alert{}}
	views, err := fleet.ListEngines(ctx, q, fleet.EngineFilter{})
	if err != nil {
		return h, err
	}
	names := make(map[uuid.UUID]string, len(views))
	engines, connected := map[uuid.UUID]int{}, map[uuid.UUID]int{}
	for _, v := range views {
		e := HealthEngine{ID: v.ID, NodeName: v.NodeName, EngineGroupName: v.EngineGroupName, Status: v.Status,
			AppliedVersion: v.AppliedVersion, TargetVersion: v.TargetVersion}
		points, err := fleet.Series(ctx, q, v.ID, time.Minute)
		if err != nil {
			return h, err
		}
		if len(points) > 0 {
			p := points[len(points)-1]
			e.QPS, e.P99Ms, e.CacheHitRatio = p.QPS, p.P99Ms, p.CacheHitRatio
		}
		fi, _, err := fleet.LatestFilterIndex(ctx, q, v.ID)
		if err != nil {
			return h, err
		}
		if fi != nil {
			e.FilterIndexBytes = fi.Bytes
		}
		h.Engines = append(h.Engines, e)
		engines[v.EngineGroupID]++
		if v.Connected {
			connected[v.EngineGroupID]++
		}
		if v.RevokedAt == nil {
			names[v.ID] = v.NodeName
		}
		if v.Status == fleet.StatusDisconnected {
			msg := "engine has never connected"
			if v.LastSeenAt != nil {
				msg = "engine disconnected; last seen " + v.LastSeenAt.UTC().Format(time.RFC3339)
			}
			h.Alerts = append(h.Alerts, Alert{AlertEngineDisconnected, SeverityCritical, v.NodeName, msg})
		}
	}

	rows, err := q.Query(ctx, "select id, name from engine_groups order by name")
	if err != nil {
		return h, store.MapError(err)
	}
	groups, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (HealthGroup, error) {
		var g HealthGroup
		err := r.Scan(&g.ID, &g.Name)
		g.Engines, g.Connected = engines[g.ID], connected[g.ID]
		return g, err
	})
	if err != nil {
		return h, store.MapError(err)
	}
	h.Groups = append(h.Groups, groups...)

	stale, err := staleCategories(ctx, q)
	if err != nil {
		return h, err
	}
	for _, key := range stale {
		h.Alerts = append(h.Alerts, Alert{AlertCategoryStale, SeverityWarning, key,
			"an enabled source failed its last refresh or has not refreshed for two intervals"})
	}

	sampleAlerts, err := recentSampleAlerts(ctx, q, names, now)
	if err != nil {
		return h, err
	}
	h.Alerts = append(h.Alerts, sampleAlerts...)
	slices.SortFunc(h.Alerts, func(a, b Alert) int {
		return cmp.Or(cmp.Compare(severityRank(a.Severity), severityRank(b.Severity)), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Subject, b.Subject))
	})
	return h, nil
}

func severityRank(s string) int {
	if s == SeverityCritical {
		return 0
	}
	return 1
}

// staleCategories returns the enabled categories with an enabled source that failed its last refresh,
// never refreshed, or is older than two refresh intervals (the rule of nexora_mgmt_filter_category_stale).
func staleCategories(ctx context.Context, q store.PolicyQuerier) ([]string, error) {
	rows, err := q.Query(ctx, `select c.key from filter_categories c join filter_lists f on f.category_key = c.key
		where c.enabled and f.enabled and (f.last_error <> '' or f.last_success_at is null
			or f.last_success_at < now() - 2 * f.refresh_interval_seconds * interval '1 second')
		group by c.key order by c.key`)
	if err != nil {
		return nil, store.MapError(err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return keys, store.MapError(err)
}

// recentSampleAlerts derives the sample-based alerts from the oldest and newest sample of each
// engine in names within the health window.
func recentSampleAlerts(ctx context.Context, q store.PolicyQuerier, names map[uuid.UUID]string, now time.Time) ([]Alert, error) {
	rows, err := q.Query(ctx, `select engine_id, stats from engine_stats where at > $1 order by engine_id, at`, now.Add(-healthWindow))
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	oldest, newest := map[uuid.UUID]*controlv1.Stats{}, map[uuid.UUID]*controlv1.Stats{}
	var order []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, store.MapError(err)
		}
		s := &controlv1.Stats{}
		if _, ok := names[id]; !ok || proto.Unmarshal(raw, s) != nil {
			continue
		}
		if oldest[id] == nil {
			oldest[id] = s
			order = append(order, id)
		}
		newest[id] = s
	}
	if err := rows.Err(); err != nil {
		return nil, store.MapError(err)
	}

	var alerts []Alert
	type upstream struct{ down, reporting int }
	upstreams := map[string]*upstream{}
	for _, id := range order {
		name, first, last := names[id], oldest[id], newest[id]
		for _, u := range last.Upstreams {
			if upstreams[u.Name] == nil {
				upstreams[u.Name] = &upstream{}
			}
			upstreams[u.Name].reporting++
			if !u.Up {
				upstreams[u.Name].down++
			}
		}
		if na := last.TlsCertificateNotAfterUnix; na > 0 {
			notAfter := time.Unix(na, 0).UTC()
			if left := notAfter.Sub(now); left < 14*24*time.Hour {
				severity := SeverityWarning
				if left < 7*24*time.Hour {
					severity = SeverityCritical
				}
				alerts = append(alerts, Alert{AlertCertificateExpiring, severity, name,
					"DNS serving certificate expires " + notAfter.Format(time.RFC3339)})
			}
		}
		for _, ta := range last.GetDnssec().GetTrustAnchors() {
			if ta.LastError != "" {
				alerts = append(alerts, Alert{AlertTrustAnchorRefreshFailed, SeverityWarning, name,
					fmt.Sprintf("trust anchor %s key %d: %s", ta.Zone, ta.KeyTag, ta.LastError)})
			}
		}
		if first.StartedUnixMs == last.StartedUnixMs {
			signals := make([]string, 0, len(last.ExportDroppedTotal))
			for signal := range last.ExportDroppedTotal {
				signals = append(signals, signal)
			}
			slices.Sort(signals)
			for _, signal := range signals {
				if n := grow(first.ExportDroppedTotal[signal], last.ExportDroppedTotal[signal]); n > 0 {
					alerts = append(alerts, Alert{AlertExportDropped, SeverityWarning, name,
						fmt.Sprintf("dropped %d %s records in the last %s", n, signal, healthWindow)})
				}
			}
		}
	}
	for name, u := range upstreams {
		if u.down > 0 {
			severity := SeverityWarning
			if u.down == u.reporting {
				severity = SeverityCritical
			}
			alerts = append(alerts, Alert{AlertUpstreamDown, severity, name,
				fmt.Sprintf("down on %d of %d engines", u.down, u.reporting)})
		}
	}
	return alerts, nil
}
