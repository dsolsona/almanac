package agentaccess

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
)

func TestAuthenticateRejectsMalformedTokenWithoutLeakingInput(t *testing.T) {
	token, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not create test token")
	}
	malformed := token.Token + "x"
	service := New("postgres://invalid/invalid", func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) })
	principal, err := service.Authenticate(t.Context(), malformed)
	if principal != (Principal{}) || !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("malformed token result=%+v error=%v", principal, err)
	}
	if strings.Contains(err.Error(), malformed) {
		t.Fatal("authentication error exposed credential material")
	}
}

func TestAuthenticatePreservesCancellation(t *testing.T) {
	token, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not create test token")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	service := New("postgres://invalid/invalid", func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) })
	principal, err := service.Authenticate(ctx, token.Token)
	if principal != (Principal{}) || !errors.Is(err, ErrCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authentication result=%+v error=%v", principal, err)
	}
}

func TestAuthenticateUnavailableStorageErrorDoesNotExposeSecrets(t *testing.T) {
	token, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not create test token")
	}
	const databaseSecret = "database-password-from-url"
	service := New("postgres://operator:"+databaseSecret+"@127.0.0.1/", func() time.Time {
		return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	})
	principal, err := service.Authenticate(t.Context(), token.Token)
	if principal != (Principal{}) || !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrCancelled) || errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("unavailable storage result=%+v error=%v", principal, err)
	}
	if strings.Contains(err.Error(), token.Token) || strings.Contains(err.Error(), databaseSecret) {
		t.Fatal("unavailable storage error exposed credential material")
	}
}
