package kwrollout

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRunSerializesPairs(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "migration"}[legacy], func(t *testing.T) {
			var events []string
			s := fakeSteps(&events, legacy)
			if err := run(context.Background(), testPairs(), s); err != nil {
				t.Fatal(err)
			}
			want := []string{"lock", "inspect", "health:existing", "apply:frozen"}
			if legacy {
				want = append(want, "health:new", "enroll", "apply:paired", "health:paired")
			}
			for _, member := range []string{"a", "b", "c", "d"} {
				want = append(want, "health:all", "apply:"+member, "health:"+member)
			}
			want = append(want, "health:desired", "apply:final", "health:final", "unlock")
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %v, want %v", events, want)
			}
		})
	}
}

func TestRunHaltsAndRetainsLockOnFailure(t *testing.T) {
	for _, phase := range []string{"inspect", "health:existing", "apply:frozen", "health:new", "enroll", "apply:paired", "health:paired", "health:all", "apply:a", "health:a", "apply:b", "health:b", "health:desired", "apply:final", "health:final"} {
		t.Run(phase, func(t *testing.T) {
			var events []string
			s := fakeSteps(&events, true)
			failure := errors.New("injected " + phase)
			inspect, health, apply, enroll := s.inspect, s.health, s.apply, s.enroll
			s.enroll = func(ctx context.Context, members []string) error {
				if err := enroll(ctx, members); err != nil {
					return err
				}
				if phase == "enroll" {
					return failure
				}
				return nil
			}
			s.inspect = func(ctx context.Context) (bool, error) {
				v, err := inspect(ctx)
				if phase == "inspect" {
					return v, failure
				}
				return v, err
			}
			s.health = func(ctx context.Context, gate Gate) error {
				if err := health(ctx, gate); err != nil {
					return err
				}
				if phase == "health:"+gate.Name {
					return failure
				}
				return nil
			}
			s.apply = func(ctx context.Context, stage Stage) error {
				if err := apply(ctx, stage); err != nil {
					return err
				}
				if phase == "apply:"+stage.Name {
					return failure
				}
				return nil
			}
			if err := run(context.Background(), testPairs(), s); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if events[len(events)-1] != phase {
				t.Fatalf("advanced after failure or released lock: %v", events)
			}
		})
	}
}

func TestRunLockContentionDoesNotInspectOrMutate(t *testing.T) {
	var events []string
	s := fakeSteps(&events, false)
	s.lock = func(context.Context) error { return errors.New("already owned") }
	if err := run(context.Background(), testPairs(), s); err == nil || len(events) != 0 {
		t.Fatalf("err=%v events=%v", err, events)
	}
}

func TestRunCancellationNeverStartsPartner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []string
	s := fakeSteps(&events, false)
	health := s.health
	s.health = func(ctx context.Context, gate Gate) error {
		err := health(ctx, gate)
		if gate.Name == "a" {
			cancel()
		}
		return err
	}
	if err := run(ctx, testPairs(), s); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	for _, e := range events {
		if e == "apply:c" || e == "apply:b" || e == "unlock" {
			t.Fatalf("advanced after cancellation: %v", events)
		}
	}
}

func TestRunRejectsOverlappingOrColocatedPairsBeforeLock(t *testing.T) {
	for _, mutate := range []func([]Pair){
		func(p []Pair) { p[1].Members[0] = p[0].Members[0] },
		func(p []Pair) { p[0].Members[1].Node = p[0].Members[0].Node },
		func(p []Pair) { p[1].Address = p[0].Address },
		func(p []Pair) { p[1].Address = "192.168.10.140" },
	} {
		p := testPairs()
		mutate(p)
		var events []string
		if err := run(context.Background(), p, fakeSteps(&events, false)); err == nil || len(events) > 0 {
			t.Fatalf("err=%v events=%v", err, events)
		}
	}
}

func TestMigrationRequiresServingPartnersBeforeRolling(t *testing.T) {
	var events []string
	s := fakeSteps(&events, true)
	paired := false
	all := []string{"a", "b", "c", "d"}
	s.enroll = func(_ context.Context, anchors []string) error {
		if !reflect.DeepEqual(anchors, []string{"a", "b"}) {
			t.Fatalf("enrolled %v", anchors)
		}
		return nil
	}
	s.health = func(_ context.Context, g Gate) error {
		required := all
		if g.Name == "existing" {
			required = []string{"a", "b"}
		}
		if !reflect.DeepEqual(g.Required, required) {
			t.Fatalf("gate %s omits a member: %v", g.Name, g.Required)
		}
		if g.Name == "paired" {
			paired = g.PairSelectors
		}
		switch g.Name {
		case "new", "paired":
			if len(g.Desired) != 0 {
				t.Fatalf("frozen resumed partners must not require replacement before cutover: %+v", g)
			}
		case "a", "b", "c", "d":
			if !reflect.DeepEqual(g.Desired, []string{g.Name}) || !g.PairSelectors {
				t.Fatalf("replacement not verified: %+v", g)
			}
		case "desired", "final":
			if !reflect.DeepEqual(g.Desired, all) || !g.PairSelectors {
				t.Fatalf("final fleet not verified: %+v", g)
			}
		}
		return nil
	}
	s.apply = func(_ context.Context, stage Stage) error {
		if stage.RollingWorkload != "" && (!paired || stage.LegacySelectors) {
			t.Fatalf("rolling before partners are reachable through VIPs: %+v", stage)
		}
		if stage.RollingWorkload == "" && stage.Name != "frozen" && stage.Name != "paired" && stage.Name != "final" {
			t.Fatalf("missing rolling target: %+v", stage)
		}
		if stage.RollingWorkload != "" && stage.RollingWorkload != stage.Name {
			t.Fatalf("wrong rolling target: %+v", stage)
		}
		return nil
	}
	if err := run(context.Background(), testPairs(), s); err != nil {
		t.Fatal(err)
	}
}

func TestRunHaltsMutationsAfterOwnershipLoss(t *testing.T) {
	for _, phase := range []string{"existing", "new", "a"} {
		t.Run(phase, func(t *testing.T) {
			store := &lockStore{}
			lock := testLock(t, store)
			var events []string
			s := fakeSteps(&events, true)
			s.lock, s.unlock, s.ownership = lock.Acquire, lock.Release, lock.Check
			lost := false
			health, apply, enroll := s.health, s.apply, s.enroll
			s.health = func(ctx context.Context, gate Gate) error {
				if gate.Name == phase {
					store.obj.Data["owner"] = "successor"
					lost = true
				}
				return health(ctx, gate)
			}
			s.apply = func(ctx context.Context, stage Stage) error {
				if lost {
					t.Fatalf("mutation after ownership loss: %+v", stage)
				}
				return apply(ctx, stage)
			}
			s.enroll = func(ctx context.Context, members []string) error {
				if lost {
					t.Fatal("enrolment after ownership loss")
				}
				return enroll(ctx, members)
			}
			if err := run(context.Background(), testPairs(), s); err == nil {
				t.Fatal("ownership loss accepted")
			}
			if store.obj.Data["owner"] != "successor" {
				t.Fatal("successor ownership changed")
			}
		})
	}
}

func fakeSteps(events *[]string, legacy bool) steps {
	return steps{
		checkDNS:        func(context.Context) error { return nil },
		monitorInterval: time.Hour,
		lock:            func(context.Context) error { *events = append(*events, "lock"); return nil },
		unlock:          func(context.Context) error { *events = append(*events, "unlock"); return nil },
		ownership:       func(context.Context) error { return nil },
		inspect:         func(context.Context) (bool, error) { *events = append(*events, "inspect"); return legacy, nil },
		enroll:          func(context.Context, []string) error { *events = append(*events, "enroll"); return nil },
		health:          func(_ context.Context, g Gate) error { *events = append(*events, "health:"+g.Name); return nil },
		apply:           func(_ context.Context, s Stage) error { *events = append(*events, "apply:"+s.Name); return nil },
	}
}

func testPairs() []Pair {
	return []Pair{
		{Name: "dns136", Address: "192.168.10.136", Members: [2]Member{{Workload: "a", Node: "master-12"}, {Workload: "c", Node: "master-11"}}},
		{Name: "dns139", Address: "192.168.10.139", Members: [2]Member{{Workload: "b", Node: "master-13"}, {Workload: "d", Node: "master-11"}}},
	}
}
