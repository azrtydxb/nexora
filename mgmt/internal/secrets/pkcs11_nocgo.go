//go:build !cgo

package secrets

import (
	"crypto"
	"errors"
)

// HSM is unavailable in builds without cgo: PKCS#11 modules are C shared libraries.
type HSM struct{ kekID []byte }

var errNoCgo = errors.New("NEXORA_PKCS11_MODULE: this nexora-mgmt build has no PKCS#11 support (built with CGO_ENABLED=0)")

func openHSM(_, _, _ string) (*HSM, error) { return nil, errNoCgo }

func (h *HSM) close() error                                         { return nil }
func (h *HSM) ensureWrapKey() error                                 { return errNoCgo }
func (h *HSM) wrapDEK(_, _ []byte) ([]byte, error)                  { return nil, errNoCgo }
func (h *HSM) unwrapDEK(_, _ []byte) ([]byte, error)                { return nil, errNoCgo }
func (h *HSM) generate(uint8, []byte) (string, error)               { return "", errNoCgo }
func (h *HSM) privateKeyAttributes([]byte) (bool, bool, error)      { return false, false, errNoCgo }
func (h *HSM) wrapKeyAttributes() (bool, bool, error)               { return false, false, errNoCgo }
func (h *HSM) destroy([]byte) error                                 { return errNoCgo }
func (h *HSM) signer(uint8, []byte, crypto.PublicKey) crypto.Signer { return nil }
