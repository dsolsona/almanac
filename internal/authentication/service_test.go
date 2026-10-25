package authentication

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage"
)

type countingHasher struct {
	verifies int
	password string
	verifier string
}

func TestNewPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := New(ctx, "postgres://local:local@127.0.0.1/almanac?sslmode=disable", time.Now)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("New with canceled context = %v", err)
	}
}

func (h *countingHasher) Hash(context.Context, string) (string, error) { return "fake-verifier", nil }
func (h *countingHasher) Verify(_ context.Context, password, verifier string) (bool, error) {
	h.verifies++
	h.password = password
	h.verifier = verifier
	return false, nil
}

func TestLoginProofUsesOneVerificationForAllSnapshotCases(t *testing.T) {
	cases := []struct {
		name         string
		snapshot     storage.LocalIdentitySnapshot
		wantVerifier string
	}{
		{name: "known", snapshot: storage.LocalIdentitySnapshot{Found: true, Enabled: true, PasswordVerifier: supportedTestVerifier}, wantVerifier: supportedTestVerifier},
		{name: "unknown", snapshot: storage.LocalIdentitySnapshot{}, wantVerifier: "dummy"},
		{name: "disabled", snapshot: storage.LocalIdentitySnapshot{Found: true, Enabled: false, PasswordVerifier: supportedTestVerifier}, wantVerifier: "dummy"},
		{name: "null", snapshot: storage.LocalIdentitySnapshot{Found: true, Enabled: true}, wantVerifier: "dummy"},
		{name: "unsupported", snapshot: storage.LocalIdentitySnapshot{Found: true, Enabled: true, PasswordVerifier: "$argon2id$unsupported"}, wantVerifier: "dummy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hasher := &countingHasher{}
			s := &Service{hasher: hasher, dummy: "dummy"}
			_, eligible, err := s.verify(t.Context(), "short", tc.snapshot)
			if err != nil || hasher.verifies != 1 || hasher.password != "short" || hasher.verifier != tc.wantVerifier {
				t.Fatalf("verify count=%d expected=1 verifier_selected=%t eligible=%t err=%v", hasher.verifies, hasher.verifier == tc.wantVerifier, eligible, err)
			}
			if eligible != (tc.name == "known") {
				t.Fatalf("eligible=%t", eligible)
			}
		})
	}
}

const supportedTestVerifier = "$argon2id$v=19$m=19456,t=2,p=1$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZzAxMjM0NTY3ODlhYmNkZWY"
