# secretbox — AES-256-GCM Encryption

The `secretbox` package provides authenticated encryption for sensitive data at rest, such as provider keys and personal access tokens. It uses AES-256-GCM (Galois/Counter Mode), which guarantees both confidentiality and authenticity.

## Behavior

Each `Encrypt` call generates a fresh random 12-byte nonce and uses AES-256-GCM to seal the plaintext. The resulting ciphertext includes a 16-byte authentication tag that prevents tampering. The nonce is prepended to the sealed ciphertext so it can be extracted during decryption without additional storage.

`Decrypt` extracts the leading nonce and uses it to open (decrypt and verify) the sealed ciphertext. If the key is wrong, the nonce is corrupted, or any byte of the ciphertext has been modified, decryption fails with an error and no plaintext is returned.

## Ciphertext Format

```
output = nonce(12 bytes) || sealed_ciphertext(len(plaintext) + 16 bytes)
```

- **Nonce**: Random 12-byte value, generated fresh per encryption
- **Sealed ciphertext**: Plaintext encrypted with AES-256-GCM, includes 16-byte authentication tag

Total output length: `12 + len(plaintext) + 16` bytes.

## Key Management

Keys are 32-byte (256-bit) values, typically loaded from configuration as 64-character hex strings via `KeyFromHex`. The key must be treated as confidential and never logged.
