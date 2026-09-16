package kwrollout

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDeploymentLockLive uses only one uniquely named ConfigMap in nexora-dev.
// Opt in explicitly; it never uses the production namespace or rollout lock.
func TestDeploymentLockLive(t *testing.T) {
	if os.Getenv("NEXORA_KW_LOCK_TEST") != "1" {
		t.Skip("set NEXORA_KW_LOCK_TEST=1 to test the real kw API in nexora-dev")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	release := "rollout-lock-test-" + strings.ToLower(rand.Text()[:12])
	a, err := NewDeploymentLock("kw", "nexora-dev", release)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewDeploymentLock("kw", "nexora-dev", release)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	uid := a.uid
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		body, err := json.Marshal(map[string]any{
			"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": uid},
		})
		if err != nil {
			t.Error(err)
			return
		}
		// Server-side UID precondition: even test cleanup must not delete a
		// recreated object if an operator changed the test fixture meanwhile.
		_, err = a.execute(cleanupCtx, body, "delete", "--raw", "/api/v1/namespaces/nexora-dev/configmaps/"+a.name, "-f", "-")
		if err != nil {
			t.Errorf("cleanup test ConfigMap %s: %v", a.name, err)
		}
	})
	if err := b.Acquire(ctx); err == nil {
		t.Fatal("real API admitted competing owner")
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
		t.Fatal("stale owner released successor")
	}
	if err := b.Check(ctx); err != nil {
		t.Fatal(err)
	}
	stale, err := b.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.changeOwner(ctx, stale, ""); err == nil {
		t.Fatal("real API accepted stale compare-and-swap release")
	}
	if err := a.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(ctx); err != nil {
		t.Fatal(err)
	}
}
