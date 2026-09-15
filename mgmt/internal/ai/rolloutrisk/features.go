// Package rolloutrisk scores the risk of a fleet rollout. Code computes every feature — what the
// rollout changes, how large and how connected the group is, how comparable past rollouts ended —
// and the model only explains the features and scores them. Nothing here changes a rollout: a
// recommendation becomes a suggest-only proposal.
package rolloutrisk

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Name is the agent name, feature label and proposal source.
const Name = "rollout_risk"

// HighRiskTypes are the audit target types whose change can break resolution fleet-wide.
var HighRiskTypes = map[string]bool{"resolution_settings": true, "dnssec_settings": true, "rpz_zone": true,
	"policy_group": true, "resolver_settings": true, "access_control": true, "zone": true}

// terminalStates are the rollout states that can serve as history.
var terminalStates = []string{"completed", "halted", "rolled_back", "superseded"}

// historyRollouts bounds how far back the history reaches, and maxSimilar how many of them the model
// sees.
const (
	historyRollouts = 100
	maxSimilar      = 5
	// maxDescription bounds one history entry's joined audit actions.
	maxDescription = 200
	// fleetWindow is how far back the current fleet SERVFAIL ratio is measured.
	fleetWindow = 15 * time.Minute
)

// Change is one configuration change of a rollout's version, as the audit log recorded it.
type Change struct {
	TargetType string `json:"target_type"`
	Action     string `json:"action"`
}

// History is how a comparable past rollout of the same engine group ended.
type History struct {
	Version          int64   `json:"config_version"`
	Description      string  `json:"description"`
	Outcome          string  `json:"outcome"`
	CanaryRejected   bool    `json:"canary_rejected"`
	HaltReason       string  `json:"halt_reason,omitempty"`
	MaxServfailRatio float64 `json:"max_servfail_ratio"`
	Similarity       float64 `json:"similarity"`
}

// Features is everything code computed about one rollout. The model interprets it; it never
// recomputes it.
type Features struct {
	RolloutID          uuid.UUID       `json:"-"`
	EngineGroupID      uuid.UUID       `json:"-"`
	Version            int64           `json:"config_version"`
	Changes            []Change        `json:"changes"`
	ChangedResources   int             `json:"changed_resources"`
	HighRisk           bool            `json:"high_risk"`
	Engines            int             `json:"engines"`
	Disconnected       int             `json:"disconnected_engines"`
	FleetServfailRatio float64         `json:"fleet_servfail_ratio"`
	Params             json.RawMessage `json:"rollout_params"`
	Similar            []History       `json:"similar_past_rollouts"`
}

// Jaccard is the size of the intersection over the size of the union of the two change sets; two
// empty sets are not similar.
func Jaccard(a, b []Change) float64 {
	set := func(cs []Change) map[Change]bool {
		m := make(map[Change]bool, len(cs))
		for _, c := range cs {
			m[c] = true
		}
		return m
	}
	x, y := set(a), set(b)
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	shared := 0
	for c := range x {
		if y[c] {
			shared++
		}
	}
	return float64(shared) / float64(len(x)+len(y)-shared)
}

// Collect reads every feature of rolloutID (store.ErrNotFound when the rollout is gone).
func Collect(ctx context.Context, q store.PolicyQuerier, rolloutID uuid.UUID, now time.Time) (Features, error) {
	f := Features{RolloutID: rolloutID, Params: json.RawMessage("{}")}
	if err := q.QueryRow(ctx, `select engine_group_id, version, params from rollouts where id = $1`, rolloutID).
		Scan(&f.EngineGroupID, &f.Version, &f.Params); err != nil {
		return Features{}, store.MapError(err)
	}
	byVersion, err := changes(ctx, q, []int64{f.Version})
	if err != nil {
		return Features{}, err
	}
	f.Changes = byVersion[f.Version]
	f.ChangedResources = len(f.Changes)
	for _, c := range f.Changes {
		if HighRiskTypes[c.TargetType] {
			f.HighRisk = true
		}
	}
	if err := q.QueryRow(ctx, `select count(*),
		count(*) filter (where e.connected_instance is null or coalesce(i.heartbeat_at > now() - interval '15 seconds', false) = false)
		from engines e left join instances i on i.id = e.connected_instance
		where e.engine_group_id = $1 and e.revoked_at is null and e.deleted_at is null`, f.EngineGroupID).
		Scan(&f.Engines, &f.Disconnected); err != nil {
		return Features{}, store.MapError(err)
	}
	fleet, err := servfailRatios(ctx, q, `select s.engine_id, s.stats from engine_stats s
		join engines e on e.id = s.engine_id
		where e.engine_group_id = $1 and s.at > $2 and s.at <= $3 order by s.engine_id, s.at`,
		f.EngineGroupID, now.Add(-fleetWindow), now)
	if err != nil {
		return Features{}, err
	}
	f.FleetServfailRatio = fleetRatio(fleet)
	if f.Similar, err = history(ctx, q, f, now); err != nil {
		return Features{}, err
	}
	return f, nil
}

// changes reads the audit rows of every version, in insertion order.
func changes(ctx context.Context, q store.PolicyQuerier, versions []int64) (map[int64][]Change, error) {
	out := map[int64][]Change{}
	if len(versions) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `select config_version, target_type, action from audit_log
		where config_version = any($1) order by config_version, id`, versions)
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int64
		var c Change
		if err := rows.Scan(&v, &c.TargetType, &c.Action); err != nil {
			return nil, store.MapError(err)
		}
		out[v] = append(out[v], c)
	}
	return out, store.MapError(rows.Err())
}

type pastRollout struct {
	id                   uuid.UUID
	version              int64
	state, strategy      string
	haltReason           string
	canaries             []uuid.UUID
	phaseStart, finished *time.Time
}

// history returns the most comparable terminal rollouts of f's engine group: the top maxSimilar by
// Jaccard similarity of their changes, ties going to the newer version.
func history(ctx context.Context, q store.PolicyQuerier, f Features, now time.Time) ([]History, error) {
	rows, err := q.Query(ctx, `select id, version, state, strategy, halt_reason, canary_engine_ids, phase_started_at, finished_at
		from rollouts where engine_group_id = $1 and id <> $2 and state = any($3)
		order by version desc limit $4`, f.EngineGroupID, f.RolloutID, terminalStates, historyRollouts)
	if err != nil {
		return nil, store.MapError(err)
	}
	past, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pastRollout, error) {
		var p pastRollout
		err := r.Scan(&p.id, &p.version, &p.state, &p.strategy, &p.haltReason, &p.canaries, &p.phaseStart, &p.finished)
		return p, err
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	if len(past) == 0 {
		return []History{}, nil
	}
	versions := make([]int64, len(past))
	for i, p := range past {
		versions[i] = p.version
	}
	byVersion, err := changes(ctx, q, versions)
	if err != nil {
		return nil, err
	}
	out := make([]History, 0, len(past))
	for _, p := range past {
		cs := byVersion[p.version]
		actions := make([]string, 0, len(cs))
		for _, c := range cs {
			actions = append(actions, c.Action)
		}
		desc := strings.Join(actions, ", ")
		if len(desc) > maxDescription {
			desc = desc[:maxDescription]
		}
		out = append(out, History{Version: p.version, Description: desc, Outcome: p.state,
			CanaryRejected: p.state == "halted" && p.strategy == "canary", HaltReason: p.haltReason,
			Similarity: Jaccard(f.Changes, cs)})
	}
	// Sort is stable on (similarity desc, version desc); the query already ordered by version desc.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	if len(out) > maxSimilar {
		out = out[:maxSimilar]
	}
	byID := map[int64]pastRollout{}
	for _, p := range past {
		byID[p.version] = p
	}
	for i := range out {
		p := byID[out[i].Version]
		ratio, err := canaryServfail(ctx, q, p, now)
		if err != nil {
			return nil, err
		}
		out[i].MaxServfailRatio = ratio
	}
	return out, nil
}

// canaryServfail is the highest SERVFAIL ratio any canary engine of p showed during its canary phase.
func canaryServfail(ctx context.Context, q store.PolicyQuerier, p pastRollout, now time.Time) (float64, error) {
	if len(p.canaries) == 0 || p.phaseStart == nil {
		return 0, nil
	}
	end := now
	if p.finished != nil {
		end = *p.finished
	}
	ratios, err := servfailRatios(ctx, q, `select engine_id, stats from engine_stats
		where engine_id = any($1) and at >= $2 and at <= $3 order by engine_id, at`, p.canaries, *p.phaseStart, end)
	if err != nil {
		return 0, err
	}
	worst := 0.0
	for _, c := range ratios {
		if c.queries > 0 {
			if r := float64(c.servfail) / float64(c.queries); r > worst {
				worst = r
			}
		}
	}
	return worst, nil
}

// counter is one engine's query and SERVFAIL delta over a window.
type counter struct{ queries, servfail uint64 }

// servfailRatios sums each engine's counter deltas over the samples sql returns, which must be
// ordered by engine and time. A counter that went down means the engine restarted, so the samples
// after it count from zero.
func servfailRatios(ctx context.Context, q store.PolicyQuerier, sql string, args ...any) (map[uuid.UUID]counter, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	out := map[uuid.UUID]counter{}
	var current uuid.UUID
	var prev *controlv1.Stats
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, store.MapError(err)
		}
		s := &controlv1.Stats{}
		if proto.Unmarshal(raw, s) != nil {
			continue
		}
		if id != current {
			current, prev = id, s
			continue
		}
		if s.QueriesTotal >= prev.QueriesTotal && s.ServfailTotal >= prev.ServfailTotal {
			c := out[id]
			c.queries += s.QueriesTotal - prev.QueriesTotal
			c.servfail += s.ServfailTotal - prev.ServfailTotal
			out[id] = c
		}
		prev = s
	}
	return out, store.MapError(rows.Err())
}

func fleetRatio(cs map[uuid.UUID]counter) float64 {
	var queries, servfail uint64
	for _, c := range cs {
		queries += c.queries
		servfail += c.servfail
	}
	if queries == 0 {
		return 0
	}
	return float64(servfail) / float64(queries)
}
