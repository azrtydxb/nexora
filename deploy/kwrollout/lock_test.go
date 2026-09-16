package kwrollout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestDeploymentLockContentionAndReuse(t *testing.T) {
	ctx := context.Background()
	store := &lockStore{}
	a, b := testLock(t, store), testLock(t, store)
	if err := a.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(ctx); err == nil {
		t.Fatal("competing invocation acquired held lock")
	}
	if err := a.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(ctx); err == nil {
		t.Fatal("stale cleanup released successor")
	}
	if err := b.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentLockReacquireRotatesOwnership(t *testing.T) {
	ctx := context.Background()
	store := &lockStore{}
	lock := testLock(t, store)
	if err := lock.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	stale := *lock
	if err := lock.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lock.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if stale.owner == lock.owner {
		t.Fatal("reused earlier ownership token")
	}
	if err := stale.Release(ctx); err == nil {
		t.Fatal("earlier ownership released reacquired lock")
	}
	if err := lock.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentLockConcurrentAcquire(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			ctx := context.Background()
			store := &lockStore{}
			if existing {
				seed := testLock(t, store)
				if err := seed.Acquire(ctx); err != nil {
					t.Fatal(err)
				}
				if err := seed.Release(ctx); err != nil {
					t.Fatal(err)
				}
			}
			a, b := testLock(t, store), testLock(t, store)
			start, results := make(chan struct{}), make(chan error, 2)
			for _, lock := range []*DeploymentLock{a, b} {
				go func() { <-start; results <- lock.Acquire(ctx) }()
			}
			close(start)
			wins := 0
			for range 2 {
				if <-results == nil {
					wins++
				}
			}
			if wins != 1 {
				t.Fatalf("successful acquisitions = %d", wins)
			}
		})
	}
}

func TestDeploymentLockStaleReleaseIsAtomic(t *testing.T) {
	for _, changeUID := range []bool{false, true} {
		t.Run(fmt.Sprint(changeUID), func(t *testing.T) {
			ctx := context.Background()
			store := &lockStore{}
			a := testLock(t, store)
			if err := a.Acquire(ctx); err != nil {
				t.Fatal(err)
			}
			// Replace ownership AFTER Release reads the object but BEFORE its patch.
			store.beforePatch = func() {
				store.obj.Data["owner"] = "successor"
				store.obj.Metadata.ResourceVersion = "99"
				if changeUID {
					store.obj.Metadata.UID = "replacement-object"
				}
			}
			if err := a.Release(ctx); err == nil {
				t.Fatal("stale release succeeded")
			}
			if store.obj.Data["owner"] != "successor" {
				t.Fatal("stale release changed successor ownership")
			}
			if err := a.Check(ctx); err == nil {
				t.Fatal("lost ownership passed check")
			}
		})
	}
}

func TestDeploymentLockAmbiguousCreateRetainsLock(t *testing.T) {
	ctx := context.Background()
	store := &lockStore{}
	a := testLock(t, store)
	a.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		out, err := store.execute(ctx, input, args...)
		if args[0] == "create" && err == nil {
			return nil, errors.New("response lost after server committed")
		}
		return out, err
	}
	if err := a.Acquire(ctx); err == nil {
		t.Fatal("ambiguous write was accepted")
	}
	if err := a.Release(ctx); err == nil {
		t.Fatal("unconfirmed ownership was released")
	}
	if err := testLock(t, store).Acquire(ctx); err == nil {
		t.Fatal("ambiguous owner was replaced")
	}
}

func TestDeploymentLockRejectsUnknownObjects(t *testing.T) {
	for _, mutate := range []func(*lockObject){
		func(o *lockObject) { delete(o.Data, "owner") },
		func(o *lockObject) { o.Data["protocol"] = "other-controller" },
		func(o *lockObject) { o.Metadata.UID = "" },
		func(o *lockObject) { o.Metadata.ResourceVersion = "" },
		func(o *lockObject) { o.Metadata.Namespace = "other" },
	} {
		store := &lockStore{}
		l := testLock(t, store)
		if err := l.Acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
		mutate(store.obj)
		if err := testLock(t, store).Acquire(context.Background()); err == nil {
			t.Fatal("unrecognized object accepted")
		}
	}
}

func TestDeploymentLockCancellationDoesNotWrite(t *testing.T) {
	store := &lockStore{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := testLock(t, store).Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if store.obj != nil {
		t.Fatal("wrote after cancellation")
	}
}

func testLock(t *testing.T, store *lockStore) *DeploymentLock {
	t.Helper()
	l, err := NewDeploymentLock("kw", "nexora-dev", "rollout-test")
	if err != nil {
		t.Fatal(err)
	}
	l.execute = store.execute
	return l
}

// lockStore models API-server create uniqueness and JSON Patch test atomicity.
// It serializes individual requests, not the client's read/modify/write sequence.
type lockStore struct {
	mu          sync.Mutex
	obj         *lockObject
	revision    int
	beforePatch func()
}

func (s *lockStore) execute(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch args[0] {
	case "get":
		if s.obj == nil {
			return nil, nil
		}
	case "create":
		if s.obj != nil {
			return nil, errors.New("AlreadyExists")
		}
		if err := json.Unmarshal(input, &s.obj); err != nil {
			return nil, err
		}
		s.obj.Metadata.UID = "object-uid"
		s.revision++
		s.obj.Metadata.ResourceVersion = fmt.Sprint(s.revision)
	case "patch":
		if s.beforePatch != nil {
			s.beforePatch()
			s.beforePatch = nil
		}
		var patch []map[string]string
		if err := json.Unmarshal(input, &patch); err != nil {
			return nil, err
		}
		if s.obj == nil || len(patch) != 5 {
			return nil, errors.New("invalid patch")
		}
		fields := map[string]string{
			"/metadata/uid": s.obj.Metadata.UID, "/metadata/resourceVersion": s.obj.Metadata.ResourceVersion,
			"/data/protocol": s.obj.Data["protocol"], "/data/owner": s.obj.Data["owner"],
		}
		for _, op := range patch[:4] {
			value, ok := fields[op["path"]]
			if !ok || op["op"] != "test" || op["value"] != value {
				return nil, errors.New("Conflict")
			}
			delete(fields, op["path"])
		}
		if patch[4]["op"] != "replace" || patch[4]["path"] != "/data/owner" {
			return nil, errors.New("unexpected mutation")
		}
		s.obj.Data["owner"] = patch[4]["value"]
		s.revision++
		s.obj.Metadata.ResourceVersion = fmt.Sprint(s.revision)
	default:
		return nil, fmt.Errorf("unexpected command %v", args)
	}
	return json.Marshal(s.obj)
}
