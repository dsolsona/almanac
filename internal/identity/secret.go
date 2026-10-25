package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

var ErrInvalidSecret = errors.New("invalid session secret")

// NewSessionSecret creates a one-time opaque 256-bit secret and its SHA-256
// verifier. Persist the verifier only; return the plaintext secret at issuance.
func NewSessionSecret() (secret string, verifier [32]byte, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", [32]byte{}, fmt.Errorf("generate session secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), sha256.Sum256(raw[:]), nil
}

// SessionSecretVerifier validates a canonical 43-character session secret and
// returns its SHA-256 verifier.
func SessionSecretVerifier(secret string) ([32]byte, error) {
	if len(secret) != 43 {
		return [32]byte{}, ErrInvalidSecret
	}
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != secret {
		return [32]byte{}, ErrInvalidSecret
	}
	return sha256.Sum256(raw), nil
}
