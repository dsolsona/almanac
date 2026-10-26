package agentaccess

import (
	"context"
	"errors"
	"fmt"
	"net"
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

func TestAuthenticateInvalidStorageConfigDoesNotExposeSecrets(t *testing.T) {
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
		t.Fatal("invalid storage configuration did not return a safe unavailable outcome")
	}
	if strings.Contains(err.Error(), token.Token) || strings.Contains(err.Error(), databaseSecret) {
		t.Fatal("unavailable storage error exposed credential material")
	}
}

func TestAuthenticateConnectionFailureDoesNotExposeSecrets(t *testing.T) {
	token, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not create test token")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("could not listen on loopback")
	}
	attempted := make(chan struct{}, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case attempted <- struct{}{}:
			default:
			}
			_ = connection.Close()
		}
	}()
	defer func() {
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("loopback listener did not stop after close")
		}
	}()

	const databaseSecret = "actual-connection-password"
	databaseURL := fmt.Sprintf("postgres://operator:%s@%s/almanac?sslmode=disable", databaseSecret, listener.Addr())
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	service := New(databaseURL, func() time.Time {
		return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	})
	principal, err := service.Authenticate(ctx, token.Token)
	if principal != (Principal{}) || !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrCancelled) || errors.Is(err, ErrInvalidCredential) {
		t.Fatal("connection failure did not return a safe unavailable outcome")
	}
	select {
	case <-attempted:
	case <-ctx.Done():
		t.Fatal("storage did not attempt a connection to the controlled listener")
	}
	if strings.Contains(err.Error(), token.Token) || strings.Contains(err.Error(), databaseSecret) {
		t.Fatal("connection failure error exposed credential material")
	}
}
