package storage

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const rotationTestPasswordVerifier = "$argon2id$v=19$m=19456,t=2,p=1$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZzAxMjM0NTY3ODlhYmNkZWY"

func TestValidateRotatePasswordInput(t *testing.T) {
	valid := RotatePasswordInput{
		CurrentSessionVerifier: [32]byte{1}, ExpectedCredentialRevision: 1,
		ExpectedCredentialEpoch: "abcdefab-cdef-4abc-8def-abcdefabcdee",
		NewPasswordVerifier:     rotationTestPasswordVerifier, NewSessionVerifier: [32]byte{2},
		Now: time.Unix(1, 0), RequestID: "request",
	}
	if err := validateRotatePasswordInput(valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	tests := map[string]func(*RotatePasswordInput){
		"zero revision":        func(v *RotatePasswordInput) { v.ExpectedCredentialRevision = 0 },
		"noncanonical epoch":   func(v *RotatePasswordInput) { v.ExpectedCredentialEpoch = strings.ToUpper(v.ExpectedCredentialEpoch) },
		"zero timestamp":       func(v *RotatePasswordInput) { v.Now = time.Time{} },
		"empty request id":     func(v *RotatePasswordInput) { v.RequestID = "" },
		"oversized request id": func(v *RotatePasswordInput) { v.RequestID = strings.Repeat("r", 129) },
		"invalid request utf8": func(v *RotatePasswordInput) { v.RequestID = "r\xff" },
		"malformed verifier":   func(v *RotatePasswordInput) { v.NewPasswordVerifier = "not-a-verifier" },
		"same session digest":  func(v *RotatePasswordInput) { v.NewSessionVerifier = v.CurrentSessionVerifier },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if err := validateRotatePasswordInput(input); !errors.Is(err, ErrInvalidSessionInput) {
				t.Fatalf("validation error = %v, want ErrInvalidSessionInput", err)
			}
		})
	}
}
