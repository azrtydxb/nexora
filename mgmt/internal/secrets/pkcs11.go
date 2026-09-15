//go:build cgo

package secrets

import (
	"crypto"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"

	"github.com/miekg/dns"
	"github.com/miekg/pkcs11"
)

const (
	hsmWrapKeyID    = "nexora-kek-v1"
	hsmWrapKeyLabel = "nexora-kek"
	// hsmSigningLabelPrefix and the installation id label every DNSSEC key object an installation
	// creates; its orphan sweeps touch only these, never another installation's keys in the token.
	hsmSigningLabelPrefix = "nexora-dnssec:"
	hsmSessions           = 4
)

// HSM is one logged-in PKCS#11 token with a small pool of read-write sessions.
type HSM struct {
	ctx          *pkcs11.Ctx
	label        string // token label, looked up again when sessions are replaced
	pinFile      string // re-read for every login, never kept in memory
	kekID        []byte
	signingLabel string // CKA_LABEL of this installation's DNSSEC key objects
	pool         chan pkcs11.SessionHandle

	mu   sync.Mutex // guards slot and gen, serialises session replacement
	slot uint
	gen  uint64 // incremented by each successful re-login
}

func openHSM(module, label, pinFile, signingLabel string) (*HSM, error) {
	pinRaw, err := readSecretFile("NEXORA_PKCS11_PIN_FILE", pinFile)
	if err != nil {
		return nil, err
	}
	defer clear(pinRaw)
	p := pkcs11.New(module)
	if p == nil {
		return nil, fmt.Errorf("NEXORA_PKCS11_MODULE %q could not be loaded", module)
	}
	if err := p.Initialize(); err != nil {
		p.Destroy()
		return nil, fmt.Errorf("pkcs11 initialize: %w", err)
	}
	h := &HSM{ctx: p, label: label, pinFile: pinFile, signingLabel: signingLabel, pool: make(chan pkcs11.SessionHandle, hsmSessions)}
	fail := func(err error) (*HSM, error) {
		_ = h.close()
		return nil, err
	}
	if h.slot, err = h.findSlot(); err != nil {
		return fail(err)
	}
	for i := 0; i < hsmSessions; i++ {
		sh, err := p.OpenSession(h.slot, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
		if err != nil {
			return fail(fmt.Errorf("pkcs11 open session: %w", err))
		}
		h.pool <- sh
		if i == 0 {
			// Login applies to every session of this application on the token.
			if err := h.login(sh, pinRaw); err != nil {
				return fail(err)
			}
		}
	}
	id := sha256.Sum256([]byte("pkcs11:" + label + ":" + hsmWrapKeyID))
	h.kekID = id[:8]
	return h, nil
}

// findSlot returns the slot holding the token labelled h.label.
func (h *HSM) findSlot() (uint, error) {
	slots, err := h.ctx.GetSlotList(true)
	if err != nil {
		return 0, fmt.Errorf("pkcs11 slot list: %w", err)
	}
	for _, s := range slots {
		if ti, err := h.ctx.GetTokenInfo(s); err == nil && strings.TrimRight(ti.Label, " \x00") == h.label {
			return s, nil
		}
	}
	return 0, fmt.Errorf("NEXORA_PKCS11_TOKEN_LABEL %q: no such token", h.label)
}

// login logs the application in as CKU_USER; it applies to every session of the application on the token.
func (h *HSM) login(sh pkcs11.SessionHandle, pinRaw []byte) error {
	if err := h.ctx.Login(sh, pkcs11.CKU_USER, strings.TrimSpace(string(pinRaw))); err != nil && !errors.Is(err, pkcs11.Error(pkcs11.CKR_USER_ALREADY_LOGGED_IN)) {
		return fmt.Errorf("pkcs11 login: %w", err)
	}
	return nil
}

func (h *HSM) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	err := h.ctx.CloseAllSessions(h.slot)
	if ferr := h.ctx.Finalize(); err == nil {
		err = ferr
	}
	h.ctx.Destroy()
	return err
}

// invalidated reports PKCS#11 errors after which a session can never succeed again.
func invalidated(err error) bool {
	for _, code := range []uint{pkcs11.CKR_SESSION_HANDLE_INVALID, pkcs11.CKR_SESSION_CLOSED, pkcs11.CKR_DEVICE_REMOVED,
		pkcs11.CKR_TOKEN_NOT_PRESENT, pkcs11.CKR_USER_NOT_LOGGED_IN} {
		if errors.Is(err, pkcs11.Error(code)) {
			return true
		}
	}
	return false
}

// with runs fn on a pooled session. A session the token invalidated (removal, reset) is replaced
// and fn retried once; a failed call changed nothing on the token, so the retry is safe.
func (h *HSM) with(fn func(sh pkcs11.SessionHandle) error) error {
	sh := <-h.pool
	gen := h.generation()
	err := fn(sh)
	if !invalidated(err) {
		h.pool <- sh
		return err
	}
	fresh, rerr := h.reopen(sh, gen)
	if rerr != nil {
		h.pool <- sh // the pool never shrinks; later calls try to recover again
		return fmt.Errorf("%w: pkcs11 session lost and not recovered: %v", ErrBackendUnavailable, rerr)
	}
	err = fn(fresh)
	h.pool <- fresh
	return err
}

func (h *HSM) generation() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gen
}

// reopen replaces the invalidated session old. It logs in again unless another call already did
// since seenGen was read, so concurrent failures from one token event log in once.
func (h *HSM) reopen(old pkcs11.SessionHandle, seenGen uint64) (pkcs11.SessionHandle, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.ctx.CloseSession(old)
	slot, err := h.findSlot() // the slot id can change when the token is inserted again
	if err != nil {
		return 0, err
	}
	h.slot = slot
	sh, err := h.ctx.OpenSession(slot, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
	if err != nil {
		return 0, fmt.Errorf("pkcs11 open session: %w", err)
	}
	if seenGen == h.gen {
		pinRaw, err := readSecretFile("NEXORA_PKCS11_PIN_FILE", h.pinFile)
		if err == nil {
			err = h.login(sh, pinRaw)
			clear(pinRaw)
		}
		if err != nil {
			_ = h.ctx.CloseSession(sh)
			return 0, err
		}
		h.gen++
	}
	return sh, nil
}

// find returns the handles of the objects of class with CKA_ID id (at most 2).
func (h *HSM) find(sh pkcs11.SessionHandle, class uint, id []byte) ([]pkcs11.ObjectHandle, error) {
	if err := h.ctx.FindObjectsInit(sh, []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_CLASS, class), pkcs11.NewAttribute(pkcs11.CKA_ID, id)}); err != nil {
		return nil, err
	}
	objs, _, err := h.ctx.FindObjects(sh, 2)
	if ferr := h.ctx.FindObjectsFinal(sh); err == nil {
		err = ferr
	}
	return objs, err
}

func (h *HSM) findOne(sh pkcs11.SessionHandle, class uint, id []byte) (pkcs11.ObjectHandle, error) {
	objs, err := h.find(sh, class, id)
	if err != nil {
		return 0, err
	}
	if len(objs) != 1 {
		return 0, fmt.Errorf("pkcs11: %d objects of class %d with id %x, want 1", len(objs), class, id)
	}
	return objs[0], nil
}

// ensureWrapKey creates the non-extractable AES-256 wrap key unless it exists. Callers serialise
// instances (main.go holds an advisory lock).
func (h *HSM) ensureWrapKey() error {
	return h.with(func(sh pkcs11.SessionHandle) error {
		objs, err := h.find(sh, pkcs11.CKO_SECRET_KEY, []byte(hsmWrapKeyID))
		if err != nil {
			return err
		}
		switch len(objs) {
		case 1:
			return nil
		case 0:
		default:
			return fmt.Errorf("pkcs11: several secret keys with id %q", hsmWrapKeyID)
		}
		_, err = h.ctx.GenerateKey(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_KEY_GEN, nil)}, []*pkcs11.Attribute{
			pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_SECRET_KEY), pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_AES),
			pkcs11.NewAttribute(pkcs11.CKA_VALUE_LEN, 32), pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
			pkcs11.NewAttribute(pkcs11.CKA_PRIVATE, true), pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, true),
			pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, false), pkcs11.NewAttribute(pkcs11.CKA_ENCRYPT, true),
			pkcs11.NewAttribute(pkcs11.CKA_DECRYPT, true), pkcs11.NewAttribute(pkcs11.CKA_ID, []byte(hsmWrapKeyID)),
			pkcs11.NewAttribute(pkcs11.CKA_LABEL, hsmWrapKeyLabel),
		})
		return err
	})
}

// gcmWrap runs AES-GCM (aad "NXE1-dek", 128-bit tag) with the wrap key over in.
func (h *HSM) gcmWrap(encrypt bool, nonce, in []byte) (out []byte, err error) {
	err = h.with(func(sh pkcs11.SessionHandle) error {
		key, err := h.findOne(sh, pkcs11.CKO_SECRET_KEY, []byte(hsmWrapKeyID))
		if err != nil {
			return fmt.Errorf("pkcs11 wrap key (EnsureHSMWrapKey not run?): %w", err)
		}
		params := pkcs11.NewGCMParams(nonce, []byte(dekAAD), 128)
		defer params.Free()
		mech := []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_GCM, params)}
		if encrypt {
			if err := h.ctx.EncryptInit(sh, mech, key); err != nil {
				return err
			}
			out, err = h.ctx.Encrypt(sh, in)
			return err
		}
		if err := h.ctx.DecryptInit(sh, mech, key); err != nil {
			return err
		}
		out, err = h.ctx.Decrypt(sh, in)
		return err
	})
	return out, err
}

func (h *HSM) wrapDEK(nonce, dek []byte) ([]byte, error) { return h.gcmWrap(true, nonce, dek) }
func (h *HSM) unwrapDEK(nonce, wrapped []byte) ([]byte, error) {
	return h.gcmWrap(false, nonce, wrapped)
}

var oidP256 = []byte{0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07}

// generate creates a token key pair with CKA_ID id; the private key is sensitive and
// non-extractable. It returns the DNSKEY public key field.
func (h *HSM) generate(alg uint8, id []byte) (publicKey string, err error) {
	err = h.with(func(sh pkcs11.SessionHandle) error {
		common := func() []*pkcs11.Attribute {
			return []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true), pkcs11.NewAttribute(pkcs11.CKA_ID, id), pkcs11.NewAttribute(pkcs11.CKA_LABEL, h.signingLabel)}
		}
		priv := append([]*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_PRIVATE, true), pkcs11.NewAttribute(pkcs11.CKA_SIGN, true),
			pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, true), pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, false)}, common()...)
		switch alg {
		case dns.ECDSAP256SHA256:
			pub := append([]*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_EC), pkcs11.NewAttribute(pkcs11.CKA_EC_PARAMS, oidP256), pkcs11.NewAttribute(pkcs11.CKA_VERIFY, true)}, common()...)
			pubH, _, err := h.ctx.GenerateKeyPair(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_EC_KEY_PAIR_GEN, nil)}, pub, priv)
			if err != nil {
				return err
			}
			attrs, err := h.ctx.GetAttributeValue(sh, pubH, []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_EC_POINT, nil)})
			if err != nil {
				return err
			}
			point := attrs[0].Value
			var inner []byte
			if _, err := asn1.Unmarshal(point, &inner); err == nil {
				point = inner
			}
			if len(point) != 65 || point[0] != 4 {
				return fmt.Errorf("pkcs11: unexpected EC point length %d", len(point))
			}
			publicKey = base64.StdEncoding.EncodeToString(point[1:])
		case dns.RSASHA256:
			pub := append([]*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_RSA), pkcs11.NewAttribute(pkcs11.CKA_MODULUS_BITS, 2048),
				pkcs11.NewAttribute(pkcs11.CKA_PUBLIC_EXPONENT, []byte{1, 0, 1}), pkcs11.NewAttribute(pkcs11.CKA_VERIFY, true)}, common()...)
			pubH, _, err := h.ctx.GenerateKeyPair(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS_KEY_PAIR_GEN, nil)}, pub, priv)
			if err != nil {
				return err
			}
			attrs, err := h.ctx.GetAttributeValue(sh, pubH, []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_PUBLIC_EXPONENT, nil), pkcs11.NewAttribute(pkcs11.CKA_MODULUS, nil)})
			if err != nil {
				return err
			}
			publicKey = rsaDNSKEYPublic(attrs[0].Value, attrs[1].Value)
		default:
			return fmt.Errorf("unsupported DNSSEC algorithm %d", alg)
		}
		return nil
	})
	return publicKey, err
}

// attributes reads CKA_EXTRACTABLE and CKA_SENSITIVE of the single object of class with id.
func (h *HSM) attributes(class uint, id []byte) (extractable, sensitive bool, err error) {
	err = h.with(func(sh pkcs11.SessionHandle) error {
		obj, err := h.findOne(sh, class, id)
		if err != nil {
			return err
		}
		attrs, err := h.ctx.GetAttributeValue(sh, obj, []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, nil), pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, nil)})
		if err != nil {
			return err
		}
		extractable = len(attrs[0].Value) == 1 && attrs[0].Value[0] != 0
		sensitive = len(attrs[1].Value) == 1 && attrs[1].Value[0] != 0
		return nil
	})
	return extractable, sensitive, err
}

func (h *HSM) privateKeyAttributes(id []byte) (bool, bool, error) {
	return h.attributes(pkcs11.CKO_PRIVATE_KEY, id)
}

func (h *HSM) wrapKeyAttributes() (bool, bool, error) {
	return h.attributes(pkcs11.CKO_SECRET_KEY, []byte(hsmWrapKeyID))
}

// destroy removes the private and public key objects with id.
func (h *HSM) destroy(id []byte) error {
	return h.with(func(sh pkcs11.SessionHandle) error {
		for _, class := range []uint{pkcs11.CKO_PRIVATE_KEY, pkcs11.CKO_PUBLIC_KEY} {
			objs, err := h.find(sh, class, id)
			if err != nil {
				return err
			}
			for _, o := range objs {
				if err := h.ctx.DestroyObject(sh, o); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// signingKeyIDs returns the distinct CKA_IDs of this installation's private and public key objects
// (labelled h.signingLabel).
func (h *HSM) signingKeyIDs() (ids [][]byte, err error) {
	err = h.with(func(sh pkcs11.SessionHandle) error {
		seen := map[string]bool{}
		for _, class := range []uint{pkcs11.CKO_PRIVATE_KEY, pkcs11.CKO_PUBLIC_KEY} {
			tmpl := []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_CLASS, class), pkcs11.NewAttribute(pkcs11.CKA_LABEL, h.signingLabel)}
			if err := h.ctx.FindObjectsInit(sh, tmpl); err != nil {
				return err
			}
			var objs []pkcs11.ObjectHandle
			for {
				batch, _, err := h.ctx.FindObjects(sh, 256)
				if err != nil {
					_ = h.ctx.FindObjectsFinal(sh)
					return err
				}
				if len(batch) == 0 {
					break
				}
				objs = append(objs, batch...)
			}
			if err := h.ctx.FindObjectsFinal(sh); err != nil {
				return err
			}
			for _, o := range objs {
				attrs, err := h.ctx.GetAttributeValue(sh, o, []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_ID, nil)})
				if err != nil {
					return err
				}
				if id := attrs[0].Value; len(id) > 0 && !seen[string(id)] {
					seen[string(id)] = true
					ids = append(ids, id)
				}
			}
		}
		return nil
	})
	return ids, err
}

var sha256DigestInfo = []byte{0x30, 0x31, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x01, 0x05, 0x00, 0x04, 0x20}

// hsmSigner signs SHA-256 digests with a token private key; the key never leaves the token.
type hsmSigner struct {
	h   *HSM
	id  []byte
	alg uint8
	pub crypto.PublicKey
}

func (h *HSM) signer(alg uint8, id []byte, pub crypto.PublicKey) crypto.Signer {
	return &hsmSigner{h: h, id: id, alg: alg, pub: pub}
}

func (s *hsmSigner) Public() crypto.PublicKey { return s.pub }

func (s *hsmSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) (sig []byte, err error) {
	if opts.HashFunc() != crypto.SHA256 || len(digest) != sha256.Size {
		return nil, fmt.Errorf("pkcs11 signer: only SHA-256 digests are supported, got %v", opts.HashFunc())
	}
	err = s.h.with(func(sh pkcs11.SessionHandle) error {
		priv, err := s.h.findOne(sh, pkcs11.CKO_PRIVATE_KEY, s.id)
		if err != nil {
			return err
		}
		switch s.alg {
		case dns.ECDSAP256SHA256:
			if err := s.h.ctx.SignInit(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_ECDSA, nil)}, priv); err != nil {
				return err
			}
			raw, err := s.h.ctx.Sign(sh, digest)
			if err != nil {
				return err
			}
			if len(raw) != 64 {
				return fmt.Errorf("pkcs11: ECDSA signature is %d bytes", len(raw))
			}
			sig, err = asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])})
			return err
		case dns.RSASHA256:
			if err := s.h.ctx.SignInit(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS, nil)}, priv); err != nil {
				return err
			}
			sig, err = s.h.ctx.Sign(sh, append(append([]byte(nil), sha256DigestInfo...), digest...))
			return err
		}
		return fmt.Errorf("unsupported algorithm %d", s.alg)
	})
	return sig, err
}
