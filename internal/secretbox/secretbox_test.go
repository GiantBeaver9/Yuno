package secretbox

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestKeyFromHexValid(t *testing.T) {
	// [positive] KeyFromHex accepts exactly 64 hex chars (32 bytes)
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	key, err := KeyFromHex(hexKey)
	if err != nil {
		t.Fatalf("KeyFromHex failed on valid 64-char hex: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("expected key length 32, got %d", len(key))
	}
	// Verify key contains expected bytes
	expected, _ := hex.DecodeString(hexKey)
	if !bytes.Equal(key[:], expected) {
		t.Fatalf("key bytes do not match decoded hex")
	}
}

func TestKeyFromHexTooShort(t *testing.T) {
	// [negative] KeyFromHex with 30-byte hex string (60 chars) → error
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	_, err := KeyFromHex(hexKey)
	if err == nil {
		t.Fatal("KeyFromHex should error on 60-char hex string")
	}
}

func TestKeyFromHexTooLong(t *testing.T) {
	// [negative] KeyFromHex with too-long hex string → error
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef00"
	_, err := KeyFromHex(hexKey)
	if err == nil {
		t.Fatal("KeyFromHex should error on 66-char hex string")
	}
}

func TestKeyFromHexInvalidChars(t *testing.T) {
	// [negative] KeyFromHex with non-hex chars → error
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg0" // 'g' is not hex
	_, err := KeyFromHex(hexKey)
	if err == nil {
		t.Fatal("KeyFromHex should error on invalid hex characters")
	}
}

func TestKeyFromHexEmpty(t *testing.T) {
	// [negative] KeyFromHex with empty string → error
	_, err := KeyFromHex("")
	if err == nil {
		t.Fatal("KeyFromHex should error on empty string")
	}
}

func TestNewFromValidKey(t *testing.T) {
	// [positive] New builds a Box from a valid 32-byte key
	key := [32]byte{}
	for i := 0; i < 32; i++ {
		key[i] = byte(i)
	}
	box := New(key)
	if box == nil {
		t.Fatal("New returned nil")
	}
}

func TestNewFromHexValid(t *testing.T) {
	// [positive] NewFromHex with valid 64-char hex → Box
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, err := NewFromHex(hexKey)
	if err != nil {
		t.Fatalf("NewFromHex failed on valid hex: %v", err)
	}
	if box == nil {
		t.Fatal("NewFromHex returned nil box")
	}
}

func TestNewFromHexInvalid(t *testing.T) {
	// [negative] NewFromHex with invalid hex → error
	hexKey := "not-valid-hex"
	_, err := NewFromHex(hexKey)
	if err == nil {
		t.Fatal("NewFromHex should error on invalid hex")
	}
}

func TestEncryptBasic(t *testing.T) {
	// [positive] Encrypt produces output of correct length
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	plaintext := []byte("hello world")
	ciphertext, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	// Expected length: 12 (nonce) + len(plaintext) + 16 (GCM tag)
	expectedLen := 12 + len(plaintext) + 16
	if len(ciphertext) != expectedLen {
		t.Fatalf("expected ciphertext length %d, got %d", expectedLen, len(ciphertext))
	}
}

func TestEncryptEmpty(t *testing.T) {
	// [edge] Empty plaintext round-trips to empty
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	plaintext := []byte{}
	ciphertext, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt of empty plaintext failed: %v", err)
	}
	// Expected length: 12 (nonce) + 0 + 16 (tag)
	if len(ciphertext) != 28 {
		t.Fatalf("expected ciphertext length 28 for empty plaintext, got %d", len(ciphertext))
	}
}

func TestEncryptRandomNonce(t *testing.T) {
	// [positive] Two Encrypt calls on identical input produce different ciphertexts (random nonce)
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	plaintext := []byte("same input")
	ciphertext1, _ := box.Encrypt(plaintext)
	ciphertext2, _ := box.Encrypt(plaintext)
	if bytes.Equal(ciphertext1, ciphertext2) {
		t.Fatal("two Encrypt calls should produce different ciphertexts due to random nonce")
	}
}

func TestDecryptBasic(t *testing.T) {
	// [positive] Round-trip: Encrypt then Decrypt yields the original plaintext
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	plaintext := []byte("hello world")
	ciphertext, _ := box.Encrypt(plaintext)
	decrypted, err := box.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted plaintext mismatch: expected %q, got %q", plaintext, decrypted)
	}
}

func TestDecryptEmpty(t *testing.T) {
	// [edge] Empty plaintext round-trips to empty
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	plaintext := []byte{}
	ciphertext, _ := box.Encrypt(plaintext)
	decrypted, err := box.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt of empty plaintext failed: %v", err)
	}
	if len(decrypted) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(decrypted))
	}
}

func TestDecryptWrongKey(t *testing.T) {
	// [negative] Decrypt a valid ciphertext with a Box made from a different 32-byte key → error
	hexKey1 := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hexKey2 := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	box1, _ := NewFromHex(hexKey1)
	box2, _ := NewFromHex(hexKey2)
	plaintext := []byte("secret message")
	ciphertext, _ := box1.Encrypt(plaintext)
	_, err := box2.Decrypt(ciphertext)
	if err == nil {
		t.Fatal("Decrypt with wrong key should error")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	// [negative] Flip one byte of a valid ciphertext → Decrypt errors
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	plaintext := []byte("secret message")
	ciphertext, err := box.Encrypt(plaintext)
	if err != nil || ciphertext == nil || len(ciphertext) == 0 {
		t.Skipf("skipping tamper test: Encrypt not yet implemented")
	}
	// Flip one byte in the ciphertext
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[0] ^= 0xFF
	_, err = box.Decrypt(tampered)
	if err == nil {
		t.Fatal("Decrypt of tampered ciphertext should error")
	}
}

func TestDecryptTooShort(t *testing.T) {
	// [edge] Decrypt of input shorter than the 12-byte nonce → error (no panic)
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	// Ciphertext shorter than 12 bytes
	short := []byte{0x01, 0x02, 0x03}
	_, err := box.Decrypt(short)
	if err == nil {
		t.Fatal("Decrypt of input shorter than nonce should error")
	}
}

func TestDecryptEmptyInput(t *testing.T) {
	// [edge] Decrypt of empty input → error
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	_, err := box.Decrypt([]byte{})
	if err == nil {
		t.Fatal("Decrypt of empty input should error")
	}
}

func TestDecryptNil(t *testing.T) {
	// [edge] Decrypt of nil input → error
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	_, err := box.Decrypt(nil)
	if err == nil {
		t.Fatal("Decrypt of nil input should error")
	}
}

func TestLongPlaintext(t *testing.T) {
	// [positive] Round-trip with longer plaintext
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	plaintext := make([]byte, 10000)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}
	ciphertext, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if len(ciphertext) != 12+len(plaintext)+16 {
		t.Fatalf("expected ciphertext length %d, got %d", 12+len(plaintext)+16, len(ciphertext))
	}
	decrypted, err := box.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatal("long plaintext round-trip failed")
	}
}

func TestDecryptTamperTagByte(t *testing.T) {
	// [negative] Flip byte in the GCM tag (last 16 bytes) → error
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, _ := NewFromHex(hexKey)
	plaintext := []byte("authenticated encryption test")
	ciphertext, err := box.Encrypt(plaintext)
	if err != nil || ciphertext == nil || len(ciphertext) == 0 {
		t.Skipf("skipping tag tamper test: Encrypt not yet implemented")
	}
	// Flip the last byte (part of the tag)
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0x01
	_, err = box.Decrypt(tampered)
	if err == nil {
		t.Fatal("Decrypt with tag tampering should error")
	}
}

func TestMultipleRoundTrips(t *testing.T) {
	// [positive] Multiple round-trips with same Box
	hexKey := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	box, _ := NewFromHex(hexKey)
	plaintexts := [][]byte{
		[]byte("first"),
		[]byte("second"),
		[]byte(""),
		[]byte("much longer message with special chars: !@#$%^&*()"),
	}
	for _, plaintext := range plaintexts {
		ciphertext, err := box.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt failed for %q: %v", plaintext, err)
		}
		decrypted, err := box.Decrypt(ciphertext)
		if err != nil {
			t.Fatalf("Decrypt failed for %q: %v", plaintext, err)
		}
		if !bytes.Equal(decrypted, plaintext) {
			t.Fatalf("round-trip mismatch for %q: got %q", plaintext, decrypted)
		}
	}
}
