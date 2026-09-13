// Package secrets seals key material at rest in NXE1 envelopes under the file key-encryption key
// named by NEXORA_KEK_FILE. It is the file-KEK half of M4's keystore, which wraps *Box and keeps the
// layout, errors and purpose format, so envelopes sealed here stay readable.
//
// NXE1 layout: "NXE1" (4) | wrap (1) | kek_id = SHA-256(KEK)[0:8] (8) | dek_nonce (12) |
// wrapped_dek = AES-256-GCM(KEK, dek_nonce, DEK, aad "NXE1-dek") (48) | data_nonce (12) |
// AES-256-GCM(DEK, data_nonce, plaintext, aad = purpose) (n+16).
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Errors callers map to API responses.
var (
	ErrUnconfigured       = errors.New("key storage unconfigured: set NEXORA_KEK_FILE")
	ErrKEKMismatch        = errors.New("envelope was sealed under a different key-encryption key")
	ErrBackendUnavailable = errors.New("requested key backend is not configured")
)

// Wrap identifiers (NXE1 byte 4).
const (
	WrapFileKEK byte = 1
	WrapPKCS11  byte = 2 // reserved for M4
)

const (
	magic      = "NXE1"
	headerLen  = 85 // magic, wrap, kek_id, dek_nonce, wrapped_dek, data_nonce
	minEnvelop = headerLen + 16
	dekAAD     = "NXE1-dek"
)

// Box seals and unseals envelopes under one file KEK. A nil or empty Box is unconfigured.
type Box struct{ kek, kekID []byte }

// LoadKEKFile reads a base64-encoded 32-byte KEK from path; "" returns an unconfigured Box.
func LoadKEKFile(path string) (*Box, error) {
	if path == "" {
		return &Box{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("NEXORA_KEK_FILE: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("NEXORA_KEK_FILE must contain 32 bytes, base64-encoded (openssl rand -base64 32)")
	}
	id := sha256.Sum256(key)
	return &Box{kek: key, kekID: id[:8]}, nil
}

// Configured reports whether a KEK is loaded (false for nil).
func (b *Box) Configured() bool { return b != nil && len(b.kek) == 32 }

// Seal encrypts plaintext under a fresh data key bound to purpose.
func (b *Box) Seal(purpose string, plaintext []byte) ([]byte, error) {
	if !b.Configured() {
		return nil, ErrUnconfigured
	}
	dek := make([]byte, 32)
	defer clear(dek)
	dekNonce, dataNonce := make([]byte, 12), make([]byte, 12)
	for _, buf := range [][]byte{dek, dekNonce, dataNonce} {
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
	}
	kekGCM, err := gcm(b.kek)
	if err != nil {
		return nil, err
	}
	wrapped := kekGCM.Seal(nil, dekNonce, dek, []byte(dekAAD))
	dataGCM, err := gcm(dek)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, headerLen+len(plaintext)+16)
	out = append(out, magic...)
	out = append(out, WrapFileKEK)
	out = append(out, b.kekID...)
	out = append(out, dekNonce...)
	out = append(out, wrapped...)
	out = append(out, dataNonce...)
	return dataGCM.Seal(out, dataNonce, plaintext, []byte(purpose)), nil
}

// Unseal authenticates and decrypts an envelope sealed for purpose.
func (b *Box) Unseal(purpose string, envelope []byte) ([]byte, error) {
	if !b.Configured() {
		return nil, ErrUnconfigured
	}
	if len(envelope) < minEnvelop || string(envelope[:4]) != magic {
		return nil, errors.New("not an NXE1 envelope")
	}
	if envelope[4] != WrapFileKEK {
		return nil, ErrBackendUnavailable
	}
	if !bytes.Equal(envelope[5:13], b.kekID) {
		return nil, ErrKEKMismatch
	}
	failed := errors.New("envelope authentication failed")
	kekGCM, err := gcm(b.kek)
	if err != nil {
		return nil, err
	}
	dek, err := kekGCM.Open(nil, envelope[13:25], envelope[25:73], []byte(dekAAD))
	if err != nil {
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
