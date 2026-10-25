// Package identity contains bounded password and opaque session-secret
// primitives used by the identity service.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var (
	// ErrInvalidPassword reports password text that is invalid under the
	// accepted UTF-8 and length policy.
	ErrInvalidPassword = errors.New("invalid password")
	// ErrInvalidVerifier reports malformed or unsupported stored verifier data.
	ErrInvalidVerifier = errors.New("invalid password verifier")
)

const (
	passwordMinRunes = 15
	passwordMaxRunes = 128
	passwordMaxBytes = 512
	argonMemoryKiB   = 19 * 1024
	argonIterations  = 2
	argonParallelism = 1
	argonSaltBytes   = 16
	argonKeyBytes    = 32
	maxVerifierBytes = 128
	verifierPrefix   = "$argon2id$v=19$m=19456,t=2,p=1$"
)

// PasswordHasher admits at most two concurrent Argon2 derivations. Share one
// instance across a server. Argon2 runs synchronously and cannot be interrupted
// mid-derivation; contexts control admission and are checked again after work.
type PasswordHasher struct {
	slots chan struct{}
}

// NewPasswordHasher returns a password hasher with bounded work admission.
func NewPasswordHasher() *PasswordHasher {
	return &PasswordHasher{slots: make(chan struct{}, 2)}
}

// ValidatePasswordText applies the accepted UTF-8 and code-point policy without
// normalizing, trimming, or otherwise changing the supplied text.
func ValidatePasswordText(password string) error {
	if len(password) > passwordMaxBytes || !utf8.ValidString(password) {
		return ErrInvalidPassword
	}
	runes := utf8.RuneCountInString(password)
	if runes < passwordMinRunes || runes > passwordMaxRunes {
		return ErrInvalidPassword
	}
	return nil
}

// Hash returns a salted Argon2id verifier using the fixed initial cost.
func (h *PasswordHasher) Hash(ctx context.Context, password string) (string, error) {
	if err := ValidatePasswordText(password); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonParallelism, argonKeyBytes)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return verifierPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

// Verify compares password against a supported verifier. Invalid candidate
// text is rejected without deriving; valid short candidates still derive.
func (h *PasswordHasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	salt, expected, err := parseVerifier(encoded)
	if err != nil {
		return false, err
	}
	if !validCandidatePassword(password) {
		return false, ErrInvalidPassword
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonParallelism, argonKeyBytes)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func (h *PasswordHasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *PasswordHasher) release() { <-h.slots }

func validCandidatePassword(password string) bool {
	return len(password) <= passwordMaxBytes && utf8.ValidString(password) && utf8.RuneCountInString(password) <= passwordMaxRunes
}

func parseVerifier(encoded string) ([]byte, []byte, error) {
	if len(encoded) > maxVerifierBytes || !strings.HasPrefix(encoded, verifierPrefix) {
		return nil, nil, ErrInvalidVerifier
	}
	parts := strings.Split(encoded[len(verifierPrefix):], "$")
	if len(parts) != 2 {
		return nil, nil, ErrInvalidVerifier
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[0])
	if err != nil || len(salt) != argonSaltBytes || base64.RawStdEncoding.EncodeToString(salt) != parts[0] {
		return nil, nil, ErrInvalidVerifier
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil || len(key) != argonKeyBytes || base64.RawStdEncoding.EncodeToString(key) != parts[1] {
		return nil, nil, ErrInvalidVerifier
	}
	return salt, key, nil
}
