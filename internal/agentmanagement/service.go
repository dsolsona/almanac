// Package agentmanagement creates agents from an unrestricted administrator session.
package agentmanagement

import (
	"context"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

type Service struct {
	databaseURL string
	auth        *authentication.Service
	now         func() time.Time
	clockMu     sync.Mutex
}

type CreateInput struct {
	SessionSecret  string `json:"-"`
	RequestID      string
	IdempotencyKey string
	Name           string
	DisplayName    string
	Lifetime       time.Duration
}

type Issuance struct {
	Creation storage.AgentCreation
	Token    string `json:"-"`
}

func New(databaseURL string, auth *authentication.Service, now func() time.Time) (*Service, error) {
	if _, err := storage.ParseDatabaseURL(databaseURL); err != nil || auth == nil || !auth.UsesDatabase(databaseURL) || now == nil {
		return nil, authentication.ErrInvalidInput
	}
	return &Service{databaseURL: databaseURL, auth: auth, now: now}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Issuance, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	proof, err := s.auth.AuthorizeAgentRegistration(ctx, authentication.AgentRegistrationSessionInput{
		SessionSecret: input.SessionSecret, RequestID: input.RequestID,
	})
	if err != nil {
		return Issuance{}, err
	}
	candidate, err := identity.NewAgentToken()
	if err != nil {
		return Issuance{}, err
	}
	creation, err := storage.CreateAgent(ctx, s.databaseURL, storage.CreateAgentInput{
		Proof: proof, Name: input.Name, DisplayName: input.DisplayName, Lifetime: input.Lifetime,
		CredentialID: candidate.ID, CredentialVerifier: candidate.Verifier,
		IdempotencyKey: input.IdempotencyKey, RequestID: input.RequestID, Now: s.currentTime,
	})
	if err != nil {
		return Issuance{}, err
	}
	if creation.Replayed {
		return Issuance{Creation: creation}, nil
	}
	if creation.Credential.ID != candidate.ID {
		return Issuance{}, storage.ErrInvalidAgentInput
	}
	return Issuance{Creation: creation, Token: candidate.Token}, nil
}

func (s *Service) currentTime() time.Time {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	return s.now()
}
