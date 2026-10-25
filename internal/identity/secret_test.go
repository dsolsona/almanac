package identity

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
)

func TestSessionSecretIssuanceAndVerifier(t *testing.T) {
	secret, verifier, err := NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 43 {
		t.Fatalf("secret length = %d, want 43", len(secret))
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("decoded secret length = %d, err = %v", len(decoded), err)
	}
	want := sha256.Sum256(decoded)
	if verifier != want {
		t.Fatal("issuance verifier is not SHA-256 of decoded secret")
	}
	got, err := SessionSecretVerifier(secret)
	if err != nil || got != want {
		t.Fatalf("SessionSecretVerifier() = %x, %v", got, err)
	}
}

func TestSessionSecretVerifierKnownVectorAndMalformedInputs(t *testing.T) {
	known := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	want := sha256.Sum256(make([]byte, 32))
	if hex.EncodeToString(want[:]) != "66687aadf862bd776c8fc18b8e9f8e20089714856ee233b3902a591d0d5f2925" {
		t.Fatal("test vector does not match the independently recorded SHA-256 digest")
	}
	got, err := SessionSecretVerifier(known)
	if err != nil || got != want {
		t.Fatalf("known vector = %x, %v", got, err)
	}
	for _, value := range []string{known + "=", " " + known, known + "\n", "!" + known[1:], known[:42], known + "x", string(make([]byte, 1025))} {
		if _, err := SessionSecretVerifier(value); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("SessionSecretVerifier malformed = %v", err)
		}
	}
}
