package agentownership

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

type Service struct {
	databaseURL string
	now         func() time.Time
}

func New(databaseURL string, now func() time.Time) *Service {
	return &Service{databaseURL: databaseURL, now: now}
}

func (s *Service) Acquire(ctx context.Context, token string, resumeSecret []byte) (Result, error) {
	if !validateProof(resumeSecret) {
		return Result{}, ErrInvalidInput
	}
	credentialID, credentialVerifier, err := s.credential(ctx, token)
	if err != nil {
		return Result{}, err
	}
	resumeVerifier := sha256.Sum256(resumeSecret)
	stored, err := storage.AcquireAgentOwnership(ctx, s.databaseURL, credentialID, credentialVerifier, resumeVerifier, s.now)
	result := publicResult(stored)
	if errors.Is(err, storage.ErrAgentOwnershipConflict) {
		conflict := &ConflictError{ReplaceAt: stored.ReplaceAt}
		return result, conflict
	}
	return result, s.mapStorageError(err)
}

func (s *Service) Resume(ctx context.Context, token string, generation int64, resumeSecret []byte) (Result, error) {
	if !validateProof(resumeSecret) || !validateGeneration(generation) {
		return Result{}, ErrInvalidInput
	}
	credentialID, credentialVerifier, err := s.credential(ctx, token)
	if err != nil {
		return Result{}, err
	}
	resumeVerifier := sha256.Sum256(resumeSecret)
	stored, err := storage.ResumeAgentOwnership(ctx, s.databaseURL, credentialID, credentialVerifier, resumeVerifier, generation, s.now)
	return publicResult(stored), s.mapStorageError(err)
}

func (s *Service) Renew(ctx context.Context, token string, generation, expectedSequence int64, resumeSecret, nonce []byte) (Result, error) {
	if !validateProof(resumeSecret) || !validateNonce(nonce) || !validateGeneration(generation) || !validateRenewalSequence(expectedSequence) {
		return Result{}, ErrInvalidInput
	}
	credentialID, credentialVerifier, err := s.credential(ctx, token)
	if err != nil {
		return Result{}, err
	}
	resumeVerifier := sha256.Sum256(resumeSecret)
	var renewalNonce [ProofSize]byte
	copy(renewalNonce[:], nonce)
	stored, err := storage.RenewAgentOwnership(ctx, s.databaseURL, credentialID, credentialVerifier, resumeVerifier,
		generation, expectedSequence, renewalNonce, s.now)
	return publicResult(stored), s.mapStorageError(err)
}

func (s *Service) credential(ctx context.Context, token string) (string, [32]byte, error) {
	if err := ctx.Err(); err != nil {
		return "", [32]byte{}, errors.Join(ErrCancelled, err)
	}
	if s == nil || s.now == nil || s.databaseURL == "" {
		return "", [32]byte{}, ErrUnavailable
	}
	credentialID, verifier, err := identity.AgentTokenVerifier(token)
	if err != nil {
		return "", [32]byte{}, ErrInvalidCredential
	}
	return credentialID, verifier, nil
}

func (s *Service) mapStorageError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrInvalidAgentCredential):
		return ErrInvalidCredential
	case errors.Is(err, storage.ErrAgentOwnershipInput):
		return ErrInvalidInput
	case errors.Is(err, storage.ErrAgentOwnershipDenied), errors.Is(err, storage.ErrAgentOwnershipExhausted):
		return ErrDenied
	case errors.Is(err, storage.ErrCancelled):
		return errors.Join(ErrCancelled, err)
	default:
		return errors.Join(ErrUnavailable, err)
	}
}

func publicResult(value storage.AgentOwnershipResult) Result {
	return Result{AgentID: value.AgentID, Generation: value.Generation, RenewalSequence: value.RenewalSequence,
		AcquiredAt: value.AcquiredAt, RenewedAt: value.RenewedAt, IssuedAt: value.IssuedAt,
		ExpiresAt: value.ExpiresAt, Nonce: append([]byte(nil), value.Nonce...), ReplaceAt: value.ReplaceAt}
}
