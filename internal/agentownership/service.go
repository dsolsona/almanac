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
	return s.AcquireWithHello(ctx, token, resumeSecret, nil)
}

func (s *Service) AcquireWithHello(ctx context.Context, token string, resumeSecret []byte, hello *storage.AgentRuntimeHello) (Result, error) {
	if !validateProof(resumeSecret) {
		return Result{}, ErrInvalidInput
	}
	credentialID, credentialVerifier, err := s.credential(ctx, token)
	if err != nil {
		return Result{}, err
	}
	resumeVerifier := sha256.Sum256(resumeSecret)
	stored, err := storage.AcquireAgentOwnershipWithHello(ctx, s.databaseURL, credentialID, credentialVerifier, resumeVerifier, hello, s.now)
	result := publicResult(stored)
	if errors.Is(err, storage.ErrAgentOwnershipConflict) {
		conflict := &ConflictError{ReplaceAt: stored.ReplaceAt}
		return result, conflict
	}
	return result, s.mapStorageError(err)
}

func (s *Service) Resume(ctx context.Context, token string, generation int64, resumeSecret []byte) (Result, error) {
	return s.ResumeWithHello(ctx, token, generation, resumeSecret, nil)
}

func (s *Service) ResumeWithHello(ctx context.Context, token string, generation int64, resumeSecret []byte, hello *storage.AgentRuntimeHello) (Result, error) {
	if !validateProof(resumeSecret) || !validateGeneration(generation) {
		return Result{}, ErrInvalidInput
	}
	credentialID, credentialVerifier, err := s.credential(ctx, token)
	if err != nil {
		return Result{}, err
	}
	resumeVerifier := sha256.Sum256(resumeSecret)
	stored, err := storage.ResumeAgentOwnershipWithHello(ctx, s.databaseURL, credentialID, credentialVerifier, resumeVerifier, generation, hello, s.now)
	return publicResult(stored), s.mapStorageError(err)
}

// RecordIncompatibleAttempt safely exposes an unsupported but structurally valid
// hello without acquiring ownership or an execution grant.
func (s *Service) RecordIncompatibleAttempt(ctx context.Context, token string, generation int64, resumeSecret []byte) (int64, error) {
	if !validateProof(resumeSecret) || generation < 0 {
		return 0, ErrInvalidInput
	}
	credentialID, credentialVerifier, err := s.credential(ctx, token)
	if err != nil {
		return 0, err
	}
	resumeVerifier := sha256.Sum256(resumeSecret)
	revision, err := storage.RecordAgentIncompatibleAttempt(ctx, s.databaseURL, credentialID, credentialVerifier,
		resumeVerifier, generation, s.now)
	if errors.Is(err, storage.ErrAgentOwnershipConflict) {
		return 0, &ConflictError{}
	}
	if err != nil {
		return 0, s.mapStorageError(err)
	}
	return revision, nil
}

func (s *Service) Renew(ctx context.Context, token string, generation, expectedSequence int64, resumeSecret, nonce []byte) (Result, error) {
	return s.RenewWithHeartbeat(ctx, token, generation, expectedSequence, resumeSecret, nonce, nil)
}

func (s *Service) RenewWithHeartbeat(ctx context.Context, token string, generation, expectedSequence int64, resumeSecret, nonce []byte, heartbeat *storage.AgentRuntimeHeartbeat) (Result, error) {
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
	stored, err := storage.RenewAgentOwnershipWithHeartbeat(ctx, s.databaseURL, credentialID, credentialVerifier, resumeVerifier,
		generation, expectedSequence, renewalNonce, heartbeat, s.now)
	return publicResult(stored), s.mapStorageError(err)
}

func (s *Service) RecordApplied(ctx context.Context, token string, generation, revision int64, resumeSecret []byte) error {
	return s.RecordAppliedProtocol(ctx, token, generation, revision, resumeSecret, "almanac.agent.v1")
}

func (s *Service) RecordAppliedProtocol(ctx context.Context, token string, generation, revision int64, resumeSecret []byte, protocol string) error {
	if !validateProof(resumeSecret) || !validateGeneration(generation) || revision <= 0 {
		return ErrInvalidInput
	}
	credentialID, credentialVerifier, err := s.credential(ctx, token)
	if err != nil {
		return err
	}
	resumeVerifier := sha256.Sum256(resumeSecret)
	if err := storage.RecordAgentConfigurationAppliedProtocol(ctx, s.databaseURL, credentialID, credentialVerifier,
		resumeVerifier, generation, revision, protocol, s.now); errors.Is(err, storage.ErrInvalidAgentCredential) {
		return ErrInvalidCredential
	} else if errors.Is(err, storage.ErrAgentOwnershipDenied) {
		return ErrDenied
	} else if errors.Is(err, storage.ErrCancelled) {
		return errors.Join(ErrCancelled, err)
	} else if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	return nil
}

func (s *Service) RecordConfigurationFailed(ctx context.Context, token string, generation, revision int64, failureCode string, resumeSecret []byte) error {
	if !validateProof(resumeSecret) || !validateGeneration(generation) || revision <= 0 {
		return ErrInvalidInput
	}
	credentialID, credentialVerifier, err := s.credential(ctx, token)
	if err != nil {
		return err
	}
	resumeVerifier := sha256.Sum256(resumeSecret)
	err = storage.RecordAgentConfigurationFailed(ctx, s.databaseURL, credentialID, credentialVerifier,
		resumeVerifier, generation, revision, failureCode, s.now)
	return s.mapStorageError(err)
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
		ExpiresAt: value.ExpiresAt, Nonce: append([]byte(nil), value.Nonce...), ReplaceAt: value.ReplaceAt,
		LeaseID: value.LeaseID, ConfigurationRevision: value.ConfigurationRevision,
		ConfigurationSnapshot: append([]byte(nil), value.ConfigurationSnapshot...),
		AllowedMeasurements:   append([]byte(nil), value.AllowedMeasurements...),
		LeaseValidFrom:        value.LeaseValidFrom, LeaseValidUntil: value.LeaseValidUntil}
}
