package control_test

import (
	"context"
	"sync"
	"testing"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// fakeOdohLoader returns the key set and digest last set.
type fakeOdohLoader struct {
	mu     sync.Mutex
	seed   []byte
	digest string
}

func (l *fakeOdohLoader) set(seed []byte, digest string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seed, l.digest = seed, digest
}

func (l *fakeOdohLoader) Load(context.Context) (*controlv1.OdohKeys, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := &controlv1.OdohKeys{}
	if l.seed != nil {
		k.Keys = []*controlv1.OdohKey{{Seed: append([]byte(nil), l.seed...), PublishAfterUnix: 1, NotAfterUnix: 2}}
	}
	return k, l.digest, nil
}

type odohHubEnv struct{ f *fixture }

// newOdohHubEnv runs one hub instance whose ODoH key loader is set before the hub starts.
func newOdohHubEnv(t *testing.T, loader control.ODoHKeyLoader) *odohHubEnv {
	e := &odohHubEnv{}
	e.f = setupHub(t, 1, func(_ *store.Store, h *control.Hub) { h.ODoH = loader })
	return e
}

// odohEngine is a connected engine stream whose server messages are collected.
type odohEngine struct{ msgs chan *controlv1.ServerMessage }

func (e *odohHubEnv) connectEngine(t *testing.T, node string) *odohEngine {
	t.Helper()
	client, id := e.f.enroll(t, e.f.addr[0])
	stream, err := client.Connect(e.f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: node}}})
	eng := &odohEngine{msgs: make(chan *controlv1.ServerMessage, 64)}
	go func() {
		for {
			m, err := stream.Recv()
			if err != nil {
				close(eng.msgs)
				return
			}
			eng.msgs <- m
		}
	}()
	return eng
}

func (e *odohHubEnv) notify(t *testing.T, channel string) {
	t.Helper()
	if _, err := e.f.st.Pool.Exec(e.f.ctx, "select pg_notify($1, '')", channel); err != nil {
		t.Fatal(err)
	}
}

// nextOdohKeys returns the next OdohKeys message, skipping any other message.
func (eng *odohEngine) nextOdohKeys(t *testing.T, within time.Duration) *controlv1.OdohKeys {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case m, ok := <-eng.msgs:
			if !ok {
				t.Fatal("stream closed")
			}
			if k := m.GetOdohKeys(); k != nil {
				return k
			}
		case <-deadline:
			t.Fatalf("no odoh keys within %s", within)
		}
	}
}

// hasOdohKeys reports whether an OdohKeys message arrives within d.
func (eng *odohEngine) hasOdohKeys(d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case m, ok := <-eng.msgs:
			if !ok {
				return false
			}
			if m.GetOdohKeys() != nil {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// A key rotation reaches every connected engine without a new config version; an unchanged set is not resent.
func TestHubOffersOdohKeysOnNotify(t *testing.T) {
	loader := &fakeOdohLoader{}
	env := newOdohHubEnv(t, loader)
	eng := env.connectEngine(t, "e1")
	if eng.hasOdohKeys(500 * time.Millisecond) {
		t.Fatal("an engine was sent an empty key set")
	}
	loader.set([]byte("seed-1"), "d1")
	env.notify(t, control.ChannelOdohKeys)
	if k := eng.nextOdohKeys(t, 5*time.Second); string(k.Keys[0].Seed) != "seed-1" {
		t.Fatalf("keys %v", k)
	}
	env.notify(t, control.ChannelOdohKeys)
	if eng.hasOdohKeys(500 * time.Millisecond) {
		t.Fatal("an unchanged key set was resent")
	}
	loader.set([]byte("seed-2"), "d2")
	env.notify(t, control.ChannelOdohKeys)
	if k := eng.nextOdohKeys(t, 5*time.Second); string(k.Keys[0].Seed) != "seed-2" {
		t.Fatalf("rotated keys %v", k)
	}
	// A reconnecting engine gets the current set with its target.
	if k := env.connectEngine(t, "e2").nextOdohKeys(t, 5*time.Second); string(k.Keys[0].Seed) != "seed-2" {
		t.Fatalf("keys on connect %v", k)
	}
}
