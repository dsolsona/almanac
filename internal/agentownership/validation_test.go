package agentownership

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServiceRejectsMalformedOperationInputsBeforeStorage(t *testing.T) {
	service := New("postgres://unreachable", func() time.Time { return time.Unix(1, 0).UTC() })
	ctx := context.Background()
	if _, err := service.Acquire(ctx, "malformed-token", bytesWithFirstByte(ProofSize)); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("malformed credential error = %v", err)
	}
	if _, err := service.Acquire(ctx, "malformed-token", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty resume proof error = %v", err)
	}
	if _, err := service.Resume(ctx, "malformed-token", 0, bytesWithFirstByte(ProofSize)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero generation error = %v", err)
	}
	if _, err := service.Renew(ctx, "malformed-token", 1, 0, bytesWithFirstByte(ProofSize), make([]byte, ProofSize)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero nonce error = %v", err)
	}
}

func TestValidateProofRequiresExactly32NonzeroBytes(t *testing.T) {
	tests := []struct {
		name  string
		proof []byte
		valid bool
	}{
		{name: "empty"},
		{name: "short", proof: make([]byte, 31)},
		{name: "long", proof: make([]byte, 33)},
		{name: "zero", proof: make([]byte, 32)},
		{name: "valid", proof: bytesWithFirstByte(32), valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validateProof(test.proof); got != test.valid {
				t.Fatalf("validateProof() = %v, want %v", got, test.valid)
			}
		})
	}
}

func TestValidateNonceRequiresExactly32NonzeroBytes(t *testing.T) {
	if validateNonce(nil) || validateNonce(make([]byte, 31)) || validateNonce(make([]byte, 32)) ||
		validateNonce(bytesWithFirstByte(33)) || !validateNonce(bytesWithFirstByte(32)) {
		t.Fatal("nonce validation did not enforce a nonzero 256-bit nonce")
	}
}

func TestValidateCountersRejectsNonpositiveAndExhaustedValues(t *testing.T) {
	if validateGeneration(0) || validateGeneration(-1) || !validateGeneration(1) ||
		!validateGeneration(int64(^uint64(0)>>1)-1) || !validateGeneration(int64(^uint64(0)>>1)) {
		t.Fatal("generation validation accepted an invalid or exhausted value")
	}
	if validateRenewalSequence(-1) || !validateRenewalSequence(0) ||
		!validateRenewalSequence(int64(^uint64(0)>>1)-1) || !validateRenewalSequence(int64(^uint64(0)>>1)) {
		t.Fatal("renewal sequence validation accepted an invalid or exhausted value")
	}
}

func TestValidateChronologyRejectsZeroBackwardAndInconsistentTimes(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	if validateChronology(time.Time{}, now, now, now.Add(time.Hour)) == nil ||
		validateChronology(now, now.Add(time.Second), now, now.Add(time.Hour)) == nil ||
		validateChronology(now, now, now.Add(time.Second), now.Add(time.Hour)) == nil ||
		validateChronology(now, now, now, now) == nil ||
		validateChronology(now, now, now, now.Add(time.Hour)) != nil {
		t.Fatal("chronology validation accepted an invalid time sequence")
	}
}

func TestSafeOutcomeErrorsDoNotIncludeProofMaterial(t *testing.T) {
	secret := "private-resume-secret"
	err := safeOutcome(ErrConflict, secret)
	if !errors.Is(err, ErrConflict) || strings.Contains(err.Error(), secret) {
		t.Fatalf("safe outcome exposed proof material or lost classification: %v", err)
	}
}

func bytesWithFirstByte(size int) []byte {
	result := make([]byte, size)
	if size > 0 {
		result[0] = 1
	}
	return result
}
