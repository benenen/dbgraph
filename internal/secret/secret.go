// Package secret seals source-database credentials so they can rest in SQLite
// without the database file itself being a credential.
//
// The key never lives in SQLite: it is read from the environment at startup, so
// a stolen database file, a backup, or a WAL fragment yields ciphertext only.
// Anything that can read the serving process's memory or environment can still
// recover the plaintext — no storage scheme changes that.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidKey reports a key that is not exactly 32 bytes of hexadecimal.
	ErrInvalidKey = errors.New("invalid secret key")
	// ErrUnreadableSecret reports ciphertext that this key cannot authenticate,
	// whether because it was tampered with or sealed under a different key.
	ErrUnreadableSecret = errors.New("secret could not be read")
)

// KeyLength is the required key size in bytes, matching AES-256.
const KeyLength = 32

// Sealed is an encrypted secret together with the identity of the key that
// sealed it, so a future key rotation can tell the two apart.
type Sealed struct {
	KeyID      string
	Ciphertext []byte
}

// Sealer encrypts and decrypts secrets with one AES-256-GCM key.
type Sealer struct {
	aead  cipher.AEAD
	keyID string
}

// NewSealer builds a Sealer from a hex-encoded 32-byte key.
func NewSealer(hexKey string) (*Sealer, error) {
	trimmed := strings.TrimSpace(hexKey)
	if len(trimmed) != hex.EncodedLen(KeyLength) {
		return nil, fmt.Errorf("%w: expected %d hexadecimal characters", ErrInvalidKey, hex.EncodedLen(KeyLength))
	}
	key, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: not hexadecimal", ErrInvalidKey)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	return &Sealer{aead: aead, keyID: deriveKeyID(key)}, nil
}

// KeyID identifies the key without revealing it. It is a truncated digest of a
// domain-separated hash of the key, so it is safe to store beside the
// ciphertext and to print in diagnostics.
func (s *Sealer) KeyID() string {
	return s.keyID
}

// Seal encrypts plaintext with a fresh random nonce, which is prefixed to the
// returned ciphertext.
func (s *Sealer) Seal(plaintext string) (Sealed, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("generate nonce: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(s.keyID))
	return Sealed{KeyID: s.keyID, Ciphertext: sealed}, nil
}

// Open authenticates and decrypts a sealed secret.
func (s *Sealer) Open(sealed Sealed) (string, error) {
	nonceSize := s.aead.NonceSize()
	if len(sealed.Ciphertext) <= nonceSize {
		return "", fmt.Errorf("%w: ciphertext is too short", ErrUnreadableSecret)
	}
	nonce := sealed.Ciphertext[:nonceSize]
	body := sealed.Ciphertext[nonceSize:]
	plaintext, err := s.aead.Open(nil, nonce, body, []byte(s.keyID))
	if err != nil {
		return "", fmt.Errorf("%w: authentication failed", ErrUnreadableSecret)
	}
	return string(plaintext), nil
}

func deriveKeyID(key []byte) string {
	digest := sha256.Sum256(append([]byte("dbgraph-secret-key-id:"), key...))
	return hex.EncodeToString(digest[:8])
}
