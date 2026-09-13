package xfrin_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/xfrin"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

func TestSchedulerLoadsOnCreateAndRefreshesOnNotify(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakePrimary{serial: 7, records: []string{"up.test. 300 IN NS ns.up.test."}, history: map[uint32][2][]string{}}
	addr := startPrimary(t, p)
	st := storetest.New(t)
	zs := &zone.Service{Store: st, Now: time.Now}
	s := &xfrin.Scheduler{Store: st, Refresher: &xfrin.Refresher{Store: st, Zones: zs, Now: time.Now, Dial: time.Second}, Tick: time.Hour}
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "up.test.", Kind: "secondary", Primaries: []zone.Endpoint{{Address: addr}}})
	if err != nil {
		t.Fatal(err)
	}
	// Tick is an hour: only the pg_notify of CreateZone and Notify can wake the scheduler in time.
	waitZone := func(want func(*zone.Zone) bool) {
		harness.Eventually(t, 10*time.Second, func() error {
			got, err := zs.GetZone(ctx, z.ID)
			if err != nil {
				return err
			}
			if !want(got) {
				return fmt.Errorf("zone: loaded=%v serial=%d trigger=%q err=%q", got.Loaded, got.Serial, got.LastTrigger, got.LastError)
			}
			return nil
		})
	}
	waitZone(func(g *zone.Zone) bool { return g.Loaded && g.Serial == 7 && g.LastTrigger == "create" })

	p.mu.Lock()
	p.serial = 8
	p.mu.Unlock()
	if err := s.Notify(ctx, "UP.test.", "198.51.100.1:53"); err == nil {
		t.Fatal("NOTIFY from a non-primary source accepted")
	}
	if err := s.Notify(ctx, "up.test.", "127.0.0.1:40000"); err != nil {
		t.Fatal(err)
	}
	waitZone(func(g *zone.Zone) bool { return g.Serial == 8 && g.LastTrigger == "notify" })
}
