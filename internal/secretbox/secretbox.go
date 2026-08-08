// Package secretbox provides AES-256-GCM encryption for sensitive data at rest.
//
// Box encrypts and decrypts provider keys and personal access tokens using authenticated
// encryption (AES-256-GCM). Each encryption operation generates a fresh random 12-byte nonce,
// which is prepended to the ciphertext. Decryption extracts the nonce and verifies the
// authentication tag; any tampering or wrong key results in an error, never partial plaintext.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// Box holds a validated 32-byte AES-256 key for encryption and decryption.
type Box struct {
	key [32]byte
}

// KeyFromHex parses a 64-character hex string into a 32-byte key.
// Non-hex or wrong-length input returns an error.
func KeyFromHex(s string) ([32]byte, error) {
	// Validate length: 64 hex chars = 32 bytes
	if len(s) != 64 {
		return [32]byte{}, fmt.Errorf("key must be exactly 64 hex characters, got %d", len(s))
	}

	// Decode hex string
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return [32]byte{}, fmt.Errorf("invalid hex string: %w", err)
	}

	// Convert to [32]byte array
	var key [32]byte
	copy(key[:], decoded)
	return key, nil
}

// New builds a Box from an already-validated 32-byte key.
func New(key [32]byte) *Box {
	return &Box{key: key}
}

// NewFromHex is KeyFromHex + New in one call.
func NewFromHex(hexKey string) (*Box, error) {
	key, err := KeyFromHex(hexKey)
	if err != nil {
		return nil, err
	}
	return New(key), nil
}

// Encrypt seals plaintext with AES-256-GCM. A fresh random 12-byte nonce is
// generated per call and PREPENDED to the returned ciphertext:
//
//	out = nonce(12) || gcm.Seal(...)
func (b *Box) Encrypt(plaintext []byte) ([]byte, error) {
	// Create AES cipher block
	block, err := aes.NewCipher(b.key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM cipher mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random 12-byte nonce
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Seal the plaintext (gcm.Seal appends tag, doesn't modify plaintext)
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	return ciphertext, nil
}

// Decrypt reverses Encrypt. Splits the leading nonce, GCM-opens the rest.
// Wrong key or any tampering (auth-tag mismatch) returns an error; never a partial plaintext.
func (b *Box) Decrypt(ciphertext []byte) ([]byte, error) {
	// Validate minimum length (12-byte nonce + at least the tag)
	if ciphertext == nil || len(ciphertext) < 12 {
		return nil, errors.New("ciphertext too short (must be at least 12 bytes for nonce)")
	}

	// Create AES cipher block
	block, err := aes.NewCipher(b.key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM cipher mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Extract nonce (first 12 bytes)
	nonce := ciphertext[:12]
	sealed := ciphertext[12:]

	// Open (decrypt and verify tag)
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}
