# secretbox — encrypt provider keys & PAT at rest

## Unit
secretbox

## Package / Owned files
`internal/secretbox/*.go`

## Deps
(none — leaf)

## Tier
simple

## Interfaces
```go
package secretbox

// Box holds a validated 32-byte AES-256 key.
type Box struct { /* key [32]byte */ }

// KeyFromHex parses a 64-char hex string (config.SecretKey) into a 32-byte key.
// Non-hex or wrong-length input → error.
func KeyFromHex(s string) ([32]byte, error)

// New builds a Box from an already-validated 32-byte key.
func New(key [32]byte) *Box

// NewFromHex is KeyFromHex + New in one call.
func NewFromHex(hexKey string) (*Box, error)

// Encrypt seals plaintext with AES-256-GCM. A fresh random 12-byte nonce is
// generated per call and PREPENDED to the returned ciphertext:
//   out = nonce(12) || gcm.Seal(...)
func (b *Box) Encrypt(plaintext []byte) ([]byte, error)

// Decrypt reverses Encrypt. Splits the leading nonce, GCM-opens the rest.
// Wrong key or any tampering (auth-tag mismatch) → error; never a partial plaintext.
func (b *Box) Decrypt(ciphertext []byte) ([]byte, error)
```

## Accept
- `Encrypt` output length == `12 + len(plaintext) + 16` (nonce + GCM tag), and differs on every call for the same plaintext (random nonce).
- `Decrypt(Encrypt(p))` == `p` for any byte slice including empty.
- `Decrypt` with a Box built from a different key returns a non-nil error and no plaintext.
- `Decrypt` of ciphertext with any byte flipped returns an error.
- `KeyFromHex` accepts exactly 64 hex chars (32 bytes); anything else errors.
- No plaintext key or plaintext secret is ever logged.

## Test cases
- **[positive]** Round-trip: `Encrypt` then `Decrypt` yields the original plaintext; two `Encrypt` calls on identical input produce different ciphertexts (nonce is random).
- **[negative]** `Decrypt` a valid ciphertext with a Box made from a *different* 32-byte key → error; flip one byte of a valid ciphertext → `Decrypt` errors. `NewFromHex` with a 30-byte hex string → error.
- **[edge]** Empty plaintext (`[]byte{}`) round-trips to empty; `Decrypt` of input shorter than the 12-byte nonce → error (no panic/slice-out-of-range).

## Salvage
Net-new (PORT.md "Net-new" #3 — the Pi repo stored all tokens as plaintext env; no encryption to copy).
