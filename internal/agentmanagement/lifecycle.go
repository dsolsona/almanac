package agentmanagement

import (
	"context"
	"net/netip"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

type GetInput struct {
	SessionSecret string `json:"-"`
	AgentID       string
}
type ListInput struct {
	SessionSecret string `json:"-"`
	Limit         int
	Cursor        string
}
type CredentialsInput struct {
	SessionSecret string `json:"-"`
	AgentID       string
	Limit         int
	Cursor        string
}
type RevokeInput struct {
	SessionSecret string `json:"-"`

	Source                                           netip.Addr
	RequestID, IdempotencyKey, AgentID, CredentialID string
	ExpectedVersion                                  int64
}
type DisableInput struct {
	SessionSecret string `json:"-"`

	Source                             netip.Addr
	RequestID, IdempotencyKey, AgentID string
	ExpectedVersion                    int64
}

func (s *Service) Get(ctx context.Context, input GetInput) (storage.AgentRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	verifier, err := identity.SessionSecretVerifier(input.SessionSecret)
	if err != nil {
		return storage.AgentRecord{}, authentication.ErrUnauthenticated
	}
	return storage.GetAgent(ctx, s.databaseURL, storage.AgentReadInput{SessionVerifier: verifier, AgentID: input.AgentID, Now: s.currentTime})
}
func (s *Service) List(ctx context.Context, input ListInput) (storage.AgentPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	verifier, err := identity.SessionSecretVerifier(input.SessionSecret)
	if err != nil {
		return storage.AgentPage{}, authentication.ErrUnauthenticated
	}
	return storage.ListAgents(ctx, s.databaseURL, storage.ListAgentsInput{SessionVerifier: verifier, Limit: input.Limit, Cursor: input.Cursor, Now: s.currentTime})
}
func (s *Service) Credentials(ctx context.Context, input CredentialsInput) (storage.AgentCredentialPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	verifier, err := identity.SessionSecretVerifier(input.SessionSecret)
	if err != nil {
		return storage.AgentCredentialPage{}, authentication.ErrUnauthenticated
	}
	return storage.ListAgentCredentials(ctx, s.databaseURL, storage.ListAgentCredentialsInput{SessionVerifier: verifier, AgentID: input.AgentID, Limit: input.Limit, Cursor: input.Cursor, Now: s.currentTime})
}
func (s *Service) Revoke(ctx context.Context, input RevokeInput) (storage.AgentLifecycleResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	proof, err := s.auth.AuthorizeAgentRegistration(ctx, authentication.AgentRegistrationSessionInput{SessionSecret: input.SessionSecret, RequestID: input.RequestID})
	if err != nil {
		return storage.AgentLifecycleResult{}, err
	}
	return storage.RevokeAgentCredential(ctx, s.databaseURL, storage.RevokeAgentCredentialInput{Proof: proof, AgentID: input.AgentID, CredentialID: input.CredentialID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: input.IdempotencyKey, RequestID: input.RequestID, Now: s.currentTime})
}
func (s *Service) Disable(ctx context.Context, input DisableInput) (storage.AgentLifecycleResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	proof, err := s.auth.AuthorizeAgentRegistration(ctx, authentication.AgentRegistrationSessionInput{SessionSecret: input.SessionSecret, RequestID: input.RequestID})
	if err != nil {
		return storage.AgentLifecycleResult{}, err
	}
	return storage.DisableAgent(ctx, s.databaseURL, storage.DisableAgentInput{Proof: proof, AgentID: input.AgentID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: input.IdempotencyKey, RequestID: input.RequestID, Now: s.currentTime})
}

func (s *Service) Dependencies(ctx context.Context, input CredentialsInput) (storage.DatasourcePage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	verifier, err := identity.SessionSecretVerifier(input.SessionSecret)
	if err != nil {
		return storage.DatasourcePage{}, authentication.ErrUnauthenticated
	}
	return storage.ListAgentDatasourceDependencies(ctx, s.databaseURL, storage.DatasourceProof{SessionVerifier: verifier}, input.AgentID, input.Limit, input.Cursor, s.currentTime)
}

func (s *Service) Delete(ctx context.Context, input DisableInput) (storage.AgentLifecycleResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	proof, err := s.auth.AuthorizeAgentRegistration(ctx, authentication.AgentRegistrationSessionInput{SessionSecret: input.SessionSecret, RequestID: input.RequestID})
	if err != nil {
		return storage.AgentLifecycleResult{}, err
	}
	return storage.DeleteAgent(ctx, s.databaseURL, storage.DisableAgentInput{Proof: proof, AgentID: input.AgentID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: input.IdempotencyKey, RequestID: input.RequestID, Now: s.currentTime})
}
