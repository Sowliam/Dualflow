package relay

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"sync/atomic"

	"github.com/flynn/noise"
)

var (
	ErrDecryptionFailed = errors.New("aead decryption or authentication failed")
	ErrSequenceExhausted = errors.New("sequence number space exhausted, rekey required")
)

// NonceSize is the 12-byte nonce size for AES-256-GCM
const NonceSize = 12

// BuildNonce constructs the 12-byte nonce from sequence number:
// [4 zero bytes (0x00, 0x00, 0x00, 0x00)] || [8 bytes big-endian seq uint64]
func BuildNonce(seq uint64) [NonceSize]byte {
	var nonce [NonceSize]byte
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}

// TransportCipher wraps Flynn Noise's noise.Cipher interface for explicit-sequence AEAD operations
type TransportCipher struct {
	cipher noise.Cipher
}

// NewTransportCipher creates a TransportCipher from a noise.Cipher
func NewTransportCipher(c noise.Cipher) *TransportCipher {
	return &TransportCipher{
		cipher: c,
	}
}

// Encrypt encrypts plaintext with explicit sequence number and associated data (AAD).
// It returns ciphertext with 16-byte authentication tag appended.
// Returns nil if cipher is uninitialized or nil.
func (tc *TransportCipher) Encrypt(seq uint64, aad, plaintext []byte) []byte {
	if tc == nil || tc.cipher == nil {
		return nil
	}
	return tc.cipher.Encrypt(nil, seq, aad, plaintext)
}

// Decrypt decrypts ciphertext with explicit sequence number and associated data (AAD).
// Returns ErrDecryptionFailed if authentication tag verification fails or cipher is uninitialized.
func (tc *TransportCipher) Decrypt(seq uint64, aad, ciphertext []byte) ([]byte, error) {
	if tc == nil || tc.cipher == nil {
		return nil, ErrDecryptionFailed
	}
	pt, err := tc.cipher.Decrypt(nil, seq, aad, ciphertext)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	return pt, nil
}

// ZeroizeKey explicitly overwrites key memory with zeros (best-effort memory hygiene).
func ZeroizeKey(k []byte) {
	for i := range k {
		k[i] = 0
	}
}

// MonotonicSequence manages atomic 64-bit sequence numbers for session frames.
type MonotonicSequence struct {
	val uint64
}

// NewMonotonicSequence creates a sequence starting at 0 (first allocated is 1).
func NewMonotonicSequence(initial uint64) *MonotonicSequence {
	return &MonotonicSequence{val: initial}
}

// Next allocates the next sequence number monotonically.
// Returns an error if sequence approaches the 2^32 boundary where rekey is required.
func (s *MonotonicSequence) Next() (uint64, error) {
	seq := atomic.AddUint64(&s.val, 1)
	if seq >= (1 << 32) {
		return seq, ErrSequenceExhausted
	}
	return seq, nil
}

// Current returns current sequence value without incrementing.
func (s *MonotonicSequence) Current() uint64 {
	return atomic.LoadUint64(&s.val)
}

// AEADDirect provides direct AES-GCM encryption when working with raw standard keys.
type AEADDirect struct {
	aead cipher.AEAD
}

// NewAEADDirect creates a wrapper over standard cipher.AEAD
func NewAEADDirect(aead cipher.AEAD) *AEADDirect {
	return &AEADDirect{aead: aead}
}

func (a *AEADDirect) Encrypt(seq uint64, aad, pt []byte) []byte {
	nonce := BuildNonce(seq)
	return a.aead.Seal(nil, nonce[:], pt, aad)
}

func (a *AEADDirect) Decrypt(seq uint64, aad, ct []byte) ([]byte, error) {
	nonce := BuildNonce(seq)
	pt, err := a.aead.Open(nil, nonce[:], ct, aad)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	return pt, nil
}

