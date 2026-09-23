package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/piwi3910/nexora/e2e/harness"
)

// NEXORA_KW_EXPECTED_ENGINES is an independently supplied inventory, never inferred
// from GET /engines. Example entry: {"id":"<UUID>","node_name":"<name>",
// "engine_group_id":"<UUID>"}. NEXORA_KW_ENGINES supplies its required cardinality.
type kwExpectedEngine struct {
	ID       string `json:"id"`
	NodeName string `json:"node_name"`
	GroupID  string `json:"engine_group_id"`
}

func kwExpectedFleet(raw string, count int) ([]kwExpectedEngine, error) {
	var expected []kwExpectedEngine
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&expected); err != nil {
		return nil, fmt.Errorf("NEXORA_KW_EXPECTED_ENGINES: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("NEXORA_KW_EXPECTED_ENGINES: trailing JSON")
	}
	if count <= 0 || len(expected) != count {
		return nil, fmt.Errorf("expected fleet cardinality %d, require positive NEXORA_KW_ENGINES=%d", len(expected), count)
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, e := range expected {
		for _, id := range []string{e.ID, e.GroupID} {
			parsed, err := uuid.Parse(id)
			if err != nil || parsed == uuid.Nil || parsed.String() != id {
				return nil, fmt.Errorf("expected inventory contains invalid canonical UUID %q", id)
			}
		}
		if strings.TrimSpace(e.NodeName) == "" || strings.TrimSpace(e.NodeName) != e.NodeName || ids[e.ID] || names[e.NodeName] {
			return nil, fmt.Errorf("empty or duplicate inventory identity: %+v", e)
		}
		ids[e.ID], names[e.NodeName] = true, true
	}
	return expected, nil
}

// Local extension of the actual /engines response; do not weaken the shared harness schema.
type kwFilterEngine struct {
	harness.EngineView
	LastSeenAt   time.Time `json:"last_seen_at"`
	PersistError string    `json:"persist_error"`
	VersionAhead bool      `json:"version_ahead"`
}

func kwFresh(at, now time.Time) bool {
	return !at.IsZero() && !at.Before(now.Add(-30*time.Second)) && !at.After(now.Add(5*time.Second))
}

// versions is nil at preflight, then pinned after category application. Each group
// can legitimately target a different version; identity and membership remain fixed.
func kwValidateFleet(expected []kwExpectedEngine, engines []kwFilterEngine, versions map[string]uint64, now time.Time) error {
	if len(expected) == 0 || len(engines) != len(expected) {
		return fmt.Errorf("fleet cardinality %d, want %d nonzero engines", len(engines), len(expected))
	}
	if versions != nil && len(versions) != len(expected) {
		return fmt.Errorf("pinned version cardinality %d, want %d", len(versions), len(expected))
	}
	want := make(map[string]kwExpectedEngine, len(expected))
	names := map[string]bool{}
	for _, e := range expected {
		if e.ID == "" || e.NodeName == "" || e.GroupID == "" || want[e.ID].ID != "" || names[e.NodeName] {
			return fmt.Errorf("invalid expected fleet identity: %+v", e)
		}
		want[e.ID], names[e.NodeName] = e, true
	}
	seen, seenNames := map[string]bool{}, map[string]bool{}
	for _, e := range engines {
		binding, exists := want[e.ID]
		if !exists || seen[e.ID] || seenNames[e.NodeName] {
			return fmt.Errorf("extra or duplicate engine %q (%s)", e.NodeName, e.ID)
		}
		seen[e.ID], seenNames[e.NodeName] = true, true
		if e.NodeName != binding.NodeName || e.EngineGroupID != binding.GroupID {
			return fmt.Errorf("engine %s identity mismatch: name=%q group=%q", e.ID, e.NodeName, e.EngineGroupID)
		}
		if !e.Connected || e.Status != "current" || e.RevokedAt != nil || e.VersionAhead || e.PersistError != "" || e.RejectedReason != "" || (e.RejectedVersion != nil && *e.RejectedVersion > e.AppliedVersion) {
			return fmt.Errorf("engine %s is not healthy/current: %+v", e.ID, e)
		}
		if !kwFresh(e.LastSeenAt, now) {
			return fmt.Errorf("engine %s stale last_seen_at: %s", e.ID, e.LastSeenAt)
		}
		if e.TargetVersion == 0 || e.AppliedVersion != e.TargetVersion || (versions != nil && versions[e.ID] != e.TargetVersion) {
			return fmt.Errorf("engine %s mismatched applied/target/pinned version: %d/%d/%d", e.ID, e.AppliedVersion, e.TargetVersion, versions[e.ID])
		}
	}
	return nil
}

func kwValidateFilterIndex(fi *kwFilterIndex, now, after time.Time) error {
	if fi == nil {
		return fmt.Errorf("missing filter_index")
	}
	if !kwFresh(fi.At, now) || fi.At.Before(after) {
		return fmt.Errorf("stale filter_index.at %s (require >= %s)", fi.At, after)
	}
	if fi.Entries < 1_000_000 || fi.Bytes <= 0 || fi.MaxBytes <= 0 || fi.Bytes > fi.MaxBytes || strings.TrimSpace(fi.CPU) == "" {
		return fmt.Errorf("incomplete filter index: %+v", fi)
	}
	for _, v := range []float64{fi.BuildSeconds, fi.DecisionNSBlocked, fi.DecisionNSClean} {
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("invalid filter index timing: %+v", fi)
		}
	}
	return nil
}

// Every retry starts from scratch. Revalidate the fleet after collecting stats so
// a disappearance or config change during the observation cannot pass acceptance.
func kwCollectFilterReport(api *harness.API, expected []kwExpectedEngine, versions map[string]uint64, after time.Time) (map[string]kwFilterIndex, error) {
	validate := func() error {
		var engines []kwFilterEngine
		if code, err := api.Do("GET", "/engines", nil, &engines); err != nil || code != 200 {
			return fmt.Errorf("GET engines: HTTP %d: %v", code, err)
		}
		return kwValidateFleet(expected, engines, versions, time.Now())
	}
	if err := validate(); err != nil {
		return nil, err
	}
	report := make(map[string]kwFilterIndex, len(expected))
	for _, e := range expected {
		var stats struct {
			FilterIndex *kwFilterIndex `json:"filter_index"`
		}
		if code, err := api.Do("GET", "/engines/"+e.ID+"/stats?window=5m", nil, &stats); err != nil || code != 200 {
			return nil, fmt.Errorf("engine %s stats: HTTP %d: %v", e.ID, code, err)
		}
		if err := kwValidateFilterIndex(stats.FilterIndex, time.Now(), after); err != nil {
			return nil, fmt.Errorf("engine %s: %w", e.ID, err)
		}
		report[e.ID] = *stats.FilterIndex
	}
	if err := validate(); err != nil {
		return nil, err
	}
	if err := kwValidateFilterReport(expected, report, time.Now(), after); err != nil {
		return nil, err
	}
	return report, nil
}

// All samples must still be fresh when the complete observation finishes.
func kwValidateFilterReport(expected []kwExpectedEngine, report map[string]kwFilterIndex, now, after time.Time) error {
	if len(report) == 0 || len(report) != len(expected) {
		return fmt.Errorf("filter report cardinality %d, want %d", len(report), len(expected))
	}
	for _, e := range expected {
		fi, ok := report[e.ID]
		if !ok {
			return fmt.Errorf("missing filter report for engine %s", e.ID)
		}
		if err := kwValidateFilterIndex(&fi, now, after); err != nil {
			return fmt.Errorf("engine %s at report completion: %w", e.ID, err)
		}
	}
	return nil
}
