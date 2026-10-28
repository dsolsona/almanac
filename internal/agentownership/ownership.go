package agentownership

import (
	"errors"
	"time"
)

const (
	ProofSize           = 32
	MaximumOwnershipAge = time.Hour
)

var (
	ErrInvalidInput      = errors.New("invalid agent ownership input")
	ErrInvalidCredential = errors.New("invalid agent credential")
	ErrDenied            = errors.New("agent ownership denied")
	ErrReplaced          = errors.Join(ErrDenied, errors.New("agent ownership replaced"))
	ErrConflict          = errors.New("agent ownership conflict")
	ErrUnavailable       = errors.New("agent ownership unavailable")
	ErrCancelled         = errors.New("agent ownership cancelled")
)

type Result struct {
	AgentID               string
	Generation            int64
	RenewalSequence       int64
	AcquiredAt            time.Time
	RenewedAt             time.Time
	IssuedAt              time.Time
	ExpiresAt             time.Time
	Nonce                 []byte
	ReplaceAt             time.Time
	LeaseID               string
	ConfigurationRevision int64
	ConfigurationSnapshot []byte
	AllowedMeasurements   []byte
	LeaseValidFrom        time.Time
	LeaseValidUntil       time.Time
}

type ConflictError struct {
	ReplaceAt time.Time
}

func (e *ConflictError) Error() string { return ErrConflict.Error() }
func (e *ConflictError) Unwrap() error { return ErrConflict }

func validateProof(proof []byte) bool { return validRandomBytes(proof) }

func validateNonce(nonce []byte) bool { return validRandomBytes(nonce) }

func validRandomBytes(value []byte) bool {
	if len(value) != ProofSize {
		return false
	}
	var nonzero byte
	for _, b := range value {
		nonzero |= b
	}
	return nonzero != 0
}

func validateGeneration(generation int64) bool {
	return generation > 0
}

func validateRenewalSequence(sequence int64) bool {
	return sequence >= 0
}

func validateChronology(now, acquiredAt, renewedAt, expiresAt time.Time) error {
	if now.IsZero() || acquiredAt.IsZero() || renewedAt.IsZero() || expiresAt.IsZero() ||
		acquiredAt.After(renewedAt) || renewedAt.After(now) || !now.Before(expiresAt) {
		return ErrInvalidInput
	}
	return nil
}

func safeOutcome(classification error, _ string) error { return classification }
