// Package secrets seals key material at rest in NXE1 envelopes and holds DNSSEC signing keys. Two
// backends exist and may be configured together: a file key-encryption key (NEXORA_KEK_FILE) and a
// PKCS#11 token (NEXORA_PKCS11_MODULE, NEXORA_PKCS11_TOKEN_LABEL, NEXORA_PKCS11_PIN_FILE).
//
// NXE1 layout: "NXE1" (4) | wrap (1) | kek_id (8) | dek_nonce (12) |
// wrapped_dek = AES-256-GCM(KEK, dek_nonce, DEK, aad "NXE1-dek") (48) | data_nonce (12) |
// AES-256-GCM(DEK, data_nonce, plaintext, aad = purpose) (n+16).
// wrap 1: file KEK, kek_id = SHA-256(KEK)[0:8]; wrap 2: the token's non-extractable AES key
// "nexora-kek", kek_id = SHA-256("pkcs11:" || token label || ":nexora-kek-v1")[0:8].
// The purpose names the row an envelope belongs to, so envelopes cannot be moved between rows.
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

// Errors callers map to API responses.
var (
	ErrUnconfigured       = errors.New("key storage unconfigured: set NEXORA_KEK_FILE or the NEXORA_PKCS11_* settings")
	ErrKEKMismatch        = errors.New("envelope was sealed under a different key-encryption key")
	ErrBackendUnavailable = errors.New("requested key backend is not configured")
)

// Wrap identifiers (NXE1 byte 4).
const (
	WrapFileKEK byte = 1
	WrapPKCS11  byte = 2
)

// Backend selects where a DNSSEC signing key lives.
type Backend string

// Backends.
const (
	BackendKEK    Backend = "kek"
	BackendPKCS11 Backend = "pkcs11"
)

const (
	magic      = "NXE1"
	headerLen  = 85 // magic, wrap, kek_id, dek_nonce, wrapped_dek, data_nonce
	minEnvelop = headerLen + 16
	dekAAD     = "NXE1-dek"
)

// Config names the key storage backends; empty fields leave a backend unconfigured.
type Config struct{ KEKFile, PKCS11Module, PKCS11TokenLabel, PKCS11PinFile string }

// Box seals and unseals envelopes and holds signing keys. A nil or empty Box is unconfigured.
type Box struct {
	kek, kekID []byte
	hsm        *HSM
}

// SigningKeyPurpose is the envelope purpose of the KEK-sealed DNSSEC private key with key_ref.
func SigningKeyPurpose(keyRef []byte) string { return "nexora/dnssec/v1:" + hex.EncodeToString(keyRef) }

// TSIGPurpose is the envelope purpose of a tsig_keys row: its id, name and algorithm, so neither
// the envelope nor the name or algorithm can be swapped between rows.
func TSIGPurpose(id uuid.UUID, name, algorithm string) string {
	return "nexora/tsig/v1:" + id.String() + ":" + name + ":" + algorithm
}

// LoadKEKFile is Open with only a KEK file ("" returns an unconfigured Box).
func LoadKEKFile(path string) (*Box, error) { return Open(Config{KEKFile: path}) }

// Open loads the configured backends. All-empty settings return an unconfigured Box; partial
// PKCS#11 settings, an unreadable or loosely permissioned KEK or PIN file, or a token that cannot
// be opened are errors.
func Open(cfg Config) (*Box, error) {
	set := 0
	for _, v := range []string{cfg.PKCS11Module, cfg.PKCS11TokenLabel, cfg.PKCS11PinFile} {
		if v != "" {
			set++
		}
	}
	if set != 0 && set != 3 {
		return nil, errors.New("NEXORA_PKCS11_MODULE, NEXORA_PKCS11_TOKEN_LABEL and NEXORA_PKCS11_PIN_FILE must be set together")
	}
	b := &Box{}
	if cfg.KEKFile != "" {
		raw, err := readSecretFile("NEXORA_KEK_FILE", cfg.KEKFile)
		if err != nil {
			return nil, err
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		clear(raw)
		if err != nil || len(key) != 32 {
			clear(key)
			return nil, errors.New("NEXORA_KEK_FILE must contain 32 bytes, base64-encoded (openssl rand -base64 32)")
		}
		id := sha256.Sum256(key)
		b.kek, b.kekID = key, id[:8]
	}
	if set == 3 {
		h, err := openHSM(cfg.PKCS11Module, cfg.PKCS11TokenLabel, cfg.PKCS11PinFile)
		if err != nil {
			return nil, err
		}
		b.hsm = h
	}
	return b, nil
}

// readSecretFile reads a secret file, refusing one that others can read or anyone but the owner
// can write. Group read is allowed only for the process's own effective group, which is how
// Kubernetes secret volumes with fsGroup present files.
func readSecretFile(setting, path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", setting, err)
	}
	perm := fi.Mode().Perm()
	groupOwned := false
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		groupOwned = int(st.Gid) == os.Getegid()
	}
	if perm&0o027 != 0 || (perm&0o040 != 0 && !groupOwned) {
		return nil, fmt.Errorf("%s %s has mode %04o: it must not be readable by other users (chmod 600)", setting, path, perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", setting, err)
	}
	return raw, nil
}

// Close releases the PKCS#11 token (no-op without one).
func (b *Box) Close() error {
	if b == nil || b.hsm == nil {
		return nil
	}
	return b.hsm.close()
}

// Configured reports whether a file KEK or a PKCS#11 token is loaded (false for nil).
func (b *Box) Configured() bool { return b.HasBackend(BackendKEK) || b.HasBackend(BackendPKCS11) }

// HasBackend reports whether be is configured.
func (b *Box) HasBackend(be Backend) bool {
	switch {
	case b == nil:
		return false
	case be == BackendKEK:
		return len(b.kek) == 32
	case be == BackendPKCS11:
		return b.hsm != nil
	}
	return false
}

// DefaultBackend is pkcs11 when a token is configured, else kek.
func (b *Box) DefaultBackend() Backend {
	if b.HasBackend(BackendPKCS11) {
		return BackendPKCS11
	}
	return BackendKEK
}

// Seal encrypts plaintext under a fresh data key bound to purpose, wrapped by the file KEK when
// configured, else by the token's wrap key.
func (b *Box) Seal(purpose string, plaintext []byte) ([]byte, error) {
	switch {
	case b.HasBackend(BackendKEK):
		return sealWith(WrapFileKEK, b.kekID, func(nonce, dek []byte) ([]byte, error) {
			g, err := gcm(b.kek)
			if err != nil {
				return nil, err
			}
			return g.Seal(nil, nonce, dek, []byte(dekAAD)), nil
		}, purpose, plaintext)
	case b.HasBackend(BackendPKCS11):
		return sealWith(WrapPKCS11, b.hsm.kekID, b.hsm.wrapDEK, purpose, plaintext)
	}
	return nil, ErrUnconfigured
}

func sealWith(wrap byte, kekID []byte, wrapDEK func(nonce, dek []byte) ([]byte, error), purpose string, plaintext []byte) ([]byte, error) {
	dek := make([]byte, 32)
	defer clear(dek)
	dekNonce, dataNonce := make([]byte, 12), make([]byte, 12)
	for _, buf := range [][]byte{dek, dekNonce, dataNonce} {
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
	}
	wrapped, err := wrapDEK(dekNonce, dek)
	if err != nil {
		return nil, err
	}
	if len(wrapped) != 48 {
		return nil, fmt.Errorf("wrapped DEK is %d bytes, want 48", len(wrapped))
	}
	dataGCM, err := gcm(dek)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, headerLen+len(plaintext)+16)
	out = append(out, magic...)
	out = append(out, wrap)
	out = append(out, kekID[:8]...)
	out = append(out, dekNonce...)
	out = append(out, wrapped...)
	out = append(out, dataNonce...)
	return dataGCM.Seal(out, dataNonce, plaintext, []byte(purpose)), nil
}

// Unseal authenticates and decrypts an envelope sealed for purpose. The caller clears the result.
func (b *Box) Unseal(purpose string, envelope []byte) ([]byte, error) {
	if !b.Configured() {
		return nil, ErrUnconfigured
	}
	if len(envelope) < minEnvelop || string(envelope[:4]) != magic {
		return nil, errors.New("not an NXE1 envelope")
	}
	var kekID []byte
	var unwrap func(nonce, wrapped []byte) ([]byte, error)
	switch {
	case envelope[4] == WrapFileKEK && b.HasBackend(BackendKEK):
		kekID = b.kekID
		unwrap = func(nonce, wrapped []byte) ([]byte, error) {
			g, err := gcm(b.kek)
			if err != nil {
				return nil, err
			}
			return g.Open(nil, nonce, wrapped, []byte(dekAAD))
		}
	case envelope[4] == WrapPKCS11 && b.HasBackend(BackendPKCS11):
		kekID, unwrap = b.hsm.kekID, b.hsm.unwrapDEK
	default:
		return nil, ErrBackendUnavailable
	}
	if !bytes.Equal(envelope[5:13], kekID) {
		return nil, ErrKEKMismatch
	}
	failed := errors.New("envelope authentication failed")
	dek, err := unwrap(envelope[13:25], envelope[25:73])
	if err != nil || len(dek) != 32 {
		clear(dek)
		return nil, failed
	}
	defer clear(dek)
	dataGCM, err := gcm(dek)
	if err != nil {
		return nil, failed
	}
	plain, err := dataGCM.Open(nil, envelope[73:85], envelope[85:], []byte(purpose))
	if err != nil {
		return nil, failed
	}
	return plain, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
