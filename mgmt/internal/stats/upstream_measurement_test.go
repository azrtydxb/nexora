package stats_test

import (
	"context"
	"testing"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func TestDashboardUnmeasuredUpstreams(t *testing.T) {
	st := storetest.New(t)
	for i, node := range []string{"first", "second"} {
		id := storetest.InsertEngine(t, st, node, store.DefaultEngineGroupID)
		mixedRTT := uint32(0)
		if i == 0 {
			mixedRTT = 20000
		}
		insertRaw(t, st, "engine_stats", "at", id, time.Now(), &controlv1.Stats{Upstreams: []*controlv1.UpstreamStatus{
			{Name: "unused", Up: true},
			{Name: "mixed", Up: true, RttUs: mixedRTT},
			{Name: "down", Up: false, RttUs: 40000},
		}})
	}
	d, err := stats.Dashboard(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]stats.UpstreamHealth{
		"unused": {Name: "unused", TotalEngines: 2, UnmeasuredEngines: 2},
		"mixed":  {Name: "mixed", TotalEngines: 2, UpEngines: 1, UnmeasuredEngines: 1, RTTMs: 20},
		"down":   {Name: "down", TotalEngines: 2},
	}
	if len(d.Upstreams) != len(want) {
		t.Fatalf("upstreams = %+v", d.Upstreams)
	}
	for _, got := range d.Upstreams {
		if got != want[got.Name] {
			t.Errorf("upstream = %+v, want %+v", got, want[got.Name])
		}
	}
}
