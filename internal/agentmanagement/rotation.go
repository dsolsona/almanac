package agentmanagement

import (
	"context"
	"net/netip"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

type RotateInput struct {
	SessionSecret                      string `json:"-"`
	CurrentPassword                    string `json:"-"`
	Source                             netip.Addr
	RequestID, IdempotencyKey, AgentID string
	ExpectedVersion                    int64
	Lifetime                           time.Duration
	Overlap                            *time.Duration
}
type RotationIssuance struct {
	Rotation storage.AgentRotationResult `json:"rotation"`
	Token    string                      `json:"-"`
}

func (s *Service) Rotate(ctx context.Context, input RotateInput) (RotationIssuance, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	proof, err := s.auth.ProveAdministrator(ctx, authentication.AdministratorProofInput{SessionSecret: input.SessionSecret, CurrentPassword: input.CurrentPassword, Source: input.Source, RequestID: input.RequestID})
	if err != nil {
		return RotationIssuance{}, err
	}
	candidate, err := identity.NewAgentToken()
	if err != nil {
		return RotationIssuance{}, err
	}
	var overlap *time.Duration
	if input.Overlap != nil {
		value := *input.Overlap
		overlap = &value
	}
	rotation, err := storage.RotateAgentCredential(ctx, s.databaseURL, storage.RotateAgentCredentialInput{Proof: proof, AgentID: input.AgentID, ExpectedVersion: input.ExpectedVersion, Lifetime: input.Lifetime, Overlap: overlap, CredentialID: candidate.ID, CredentialVerifier: candidate.Verifier, IdempotencyKey: input.IdempotencyKey, RequestID: input.RequestID, Now: s.currentTime})
	if err != nil {
		return RotationIssuance{}, err
	}
	if rotation.Replayed {
		return RotationIssuance{Rotation: rotation}, nil
	}
	if rotation.Credential.ID != candidate.ID {
		return RotationIssuance{}, storage.ErrInvalidAgentInput
	}
	return RotationIssuance{Rotation: rotation, Token: candidate.Token}, nil
}
