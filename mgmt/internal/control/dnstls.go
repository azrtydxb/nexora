package control

import (
	"sync"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

// DNSTLSFanout delivers the DNS serving certificate to the engines connected to this instance.
// Each engine has a capacity-1 channel: a newer certificate replaces an undelivered older one.
type DNSTLSFanout struct {
	mu      sync.Mutex
	current *pki.DNSTLSMaterial
	engines map[string]*tlsEngine
}

type tlsEngine struct {
	fingerprint string // what the engine holds, as far as this instance knows
	ch          chan *controlv1.TlsMaterial
}

// NewDNSTLSFanout creates an empty fan-out.
func NewDNSTLSFanout() *DNSTLSFanout {
	return &DNSTLSFanout{engines: map[string]*tlsEngine{}}
}

// Register adds an engine that holds helloFingerprint ("" for none) and queues the current
// material when it differs.
func (f *DNSTLSFanout) Register(engineID, helloFingerprint string) <-chan *controlv1.TlsMaterial {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := &tlsEngine{fingerprint: helloFingerprint, ch: make(chan *controlv1.TlsMaterial, 1)}
	f.engines[engineID] = e
	f.queue(e)
	return e.ch
}

// Unregister removes the registration that returned ch; its channel receives nothing more. A newer
// registration of the same engine (a reconnect before the old stream ended) stays.
func (f *DNSTLSFanout) Unregister(engineID string, ch <-chan *controlv1.TlsMaterial) {
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
		f.queue(e)
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

// queue never blocks: it drains an undelivered value before sending. Callers hold f.mu.
func (f *DNSTLSFanout) queue(e *tlsEngine) {
	if f.current == nil || f.current.FingerprintSHA256 == e.fingerprint {
		return
	}
	select {
	case <-e.ch:
	default:
	}
	e.ch <- &controlv1.TlsMaterial{
		CertificateChainPem: f.current.ChainPEM,
		PrivateKeyPem:       f.current.KeyPEM,
		FingerprintSha256:   f.current.FingerprintSHA256,
	}
}
