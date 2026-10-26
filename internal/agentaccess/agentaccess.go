package agentaccess

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

var (
	ErrInvalidCredential = errors.New("invalid agent credential")
	ErrUnavailable       = errors.New("agent authentication unavailable")
	ErrCancelled         = errors.New("agent authentication cancelled")
)

type Principal struct {
	AgentID      string
	CredentialID string
	ExpiresAt    time.Time
}

type Service struct {
	databaseURL string
	now         func() time.Time
}

func New(databaseURL string, now func() time.Time) *Service {
	return &Service{databaseURL: databaseURL, now: now}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, errors.Join(ErrCancelled, err)
	}
	if s == nil || s.now == nil {
		return Principal{}, ErrUnavailable
	}
	credentialID, verifier, err := identity.AgentTokenVerifier(token)
	if err != nil {
		return Principal{}, ErrInvalidCredential
	}
	result, err := storage.AuthenticateAgentCredential(ctx, s.databaseURL, credentialID, verifier, s.now)
	if errors.Is(err, storage.ErrInvalidAgentCredential) {
		return Principal{}, ErrInvalidCredential
	}
	if errors.Is(err, storage.ErrCancelled) {
		return Principal{}, errors.Join(ErrCancelled, err)
	}
	if err != nil {
		return Principal{}, errors.Join(ErrUnavailable, err)
	}
	return Principal{AgentID: result.AgentID, CredentialID: result.CredentialID, ExpiresAt: result.ExpiresAt}, nil
}
