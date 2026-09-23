package control

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// DNSTLSFanout delivers the DNS serving certificate to the engines connected to this instance.
// Each engine has a capacity-1 channel: a newer certificate replaces an undelivered older one.
type DNSTLSFanout struct {
	mu      sync.Mutex
	st      *store.Store
	current *pki.DNSTLSMaterial
	engines map[string]*tlsEngine
}

type tlsEngine struct {
	id, sessionID uuid.UUID
	serial        string
	fingerprint   string // what the engine holds, as far as this instance knows
	ch            chan Delivery[*controlv1.TlsMaterial]
}

// NewDNSTLSFanout creates an empty fan-out. Production supplies its store here;
// NewServer also binds its store before serving. Without a store offers fail closed.
func NewDNSTLSFanout(st ...*store.Store) *DNSTLSFanout {
	f := &DNSTLSFanout{engines: map[string]*tlsEngine{}}
	if len(st) > 0 {
		f.st = st[0]
	}
	return f
}

// Register adds an engine that holds helloFingerprint ("" for none) and queues the current
// material when it differs.
func (f *DNSTLSFanout) Register(ctx context.Context, engineID string, sessionID uuid.UUID, serial, helloFingerprint string) (<-chan Delivery[*controlv1.TlsMaterial], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, err := uuid.Parse(engineID)
	if err != nil {
		return nil, err
	}
	e := &tlsEngine{id: id, sessionID: sessionID, serial: serial, fingerprint: helloFingerprint, ch: make(chan Delivery[*controlv1.TlsMaterial], 1)}
	// Membership and initial enqueue share the fence: a delayed old Register
	// must not replace the legitimate registration even if current is nil.
	previous := f.engines[engineID]
	err = enqueueSecret(ctx, f.st, id, sessionID, serial, func(barrier *commitBarrier) {
		f.engines[engineID] = e
		f.queue(e, barrier)
	})
	if err != nil {
		if previous == nil {
			delete(f.engines, engineID)
		} else {
			f.engines[engineID] = previous
		}
	}
	return e.ch, err
}

// Unregister removes the registration that returned ch; its channel receives nothing more. A newer
// registration of the same engine (a reconnect before the old stream ended) stays.
func (f *DNSTLSFanout) Unregister(engineID string, ch <-chan Delivery[*controlv1.TlsMaterial]) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.engines[engineID]; ok && e.ch == ch {
		delete(f.engines, engineID)
	}
}

// Set replaces the current material and queues it to every engine holding another certificate.
func (f *DNSTLSFanout) Set(m *pki.DNSTLSMaterial) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = m
	for _, e := range f.engines {
		if err := enqueueSecret(context.Background(), f.st, e.id, e.sessionID, e.serial, func(barrier *commitBarrier) { f.queue(e, barrier) }); err != nil {
			slog.Debug("fence dns tls", "engine", e.id, "err", err)
		}
	}
}

// Result records an engine's answer; an applied certificate is not pushed to it again.
func (f *DNSTLSFanout) Result(engineID string, r *controlv1.TlsMaterialResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.engines[engineID]; ok && r.Applied {
		e.fingerprint = r.FingerprintSha256
	}
}

// Current is the material loaded by this instance, or nil.
func (f *DNSTLSFanout) Current() *pki.DNSTLSMaterial {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

// queue runs only inside enqueueSecret and never blocks: it drains an undelivered value before sending. Callers hold f.mu.
func (f *DNSTLSFanout) queue(e *tlsEngine, barrier *commitBarrier) {
	if f.current == nil || f.current.FingerprintSHA256 == e.fingerprint {
		return
	}
	select {
	case <-e.ch:
	default:
	}
	select {
	case e.ch <- Delivery[*controlv1.TlsMaterial]{barrier: barrier, value: &controlv1.TlsMaterial{
		CertificateChainPem: f.current.ChainPEM,
		PrivateKeyPem:       f.current.KeyPEM,
		FingerprintSha256:   f.current.FingerprintSHA256,
	}}:
	default:
	}
}

// ResultFor applies a committed result only to the registration that received it.
// A delayed result must not change a replacement stream's in-memory fingerprint.
func (f *DNSTLSFanout) ResultFor(engineID string, ch <-chan Delivery[*controlv1.TlsMaterial], r *controlv1.TlsMaterialResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.engines[engineID]; ok && e.ch == ch && r.Applied {
		e.fingerprint = r.FingerprintSha256
	}
}

// RetryFor reoffers the latest unacknowledged material only to this registration.
// Every attempt performs fresh persisted authorization, including after ambiguity.
func (f *DNSTLSFanout) RetryFor(ctx context.Context, engineID string, ch <-chan Delivery[*controlv1.TlsMaterial]) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.engines[engineID]
	if e == nil || e.ch != ch || f.current == nil || f.current.FingerprintSHA256 == e.fingerprint {
		return
	}
	if err := enqueueSecret(ctx, f.st, e.id, e.sessionID, e.serial, func(b *commitBarrier) { f.queue(e, b) }); err != nil {
		slog.Debug("retry dns tls fence", "engine", engineID, "err", err)
	}
}
