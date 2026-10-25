package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionOperationsRejectMalformedInputsBeforeDatabaseAccess(t *testing.T) {
	ctx := context.Background()
	badURL := "not-a-database-url"
	if _, err := ReadLocalIdentity(ctx, badURL, "bad\xff"); !errors.Is(err, ErrInvalidSessionInput) {
		t.Fatalf("invalid login error = %v, want ErrInvalidSessionInput", err)
	}
	if _, err := ReadLocalIdentity(ctx, badURL, strings.Repeat("x", 257)); !errors.Is(err, ErrInvalidSessionInput) {
		t.Fatalf("oversized login error = %v, want ErrInvalidSessionInput", err)
	}
	base := IssueSessionInput{
		UserID:                     "abcdefab-cdef-4abc-8def-abcdefabcdef",
		ExpectedCredentialEpoch:    "abcdefab-cdef-4abc-8def-abcdefabcdee",
		ExpectedCredentialRevision: 1, Now: time.Unix(1, 0), RequestID: "request",
	}
	cases := map[string]IssueSessionInput{
		"noncanonical user UUID": func() IssueSessionInput { v := base; v.UserID = strings.ToUpper(v.UserID); return v }(),
		"noncanonical epoch UUID": func() IssueSessionInput {
			v := base
			v.ExpectedCredentialEpoch = strings.ToUpper(v.ExpectedCredentialEpoch)
			return v
		}(),
		"zero revision":         func() IssueSessionInput { v := base; v.ExpectedCredentialRevision = 0; return v }(),
		"zero time":             func() IssueSessionInput { v := base; v.Now = time.Time{}; return v }(),
		"empty request":         func() IssueSessionInput { v := base; v.RequestID = ""; return v }(),
		"oversized request":     func() IssueSessionInput { v := base; v.RequestID = strings.Repeat("r", 129); return v }(),
		"invalid UTF-8 request": func() IssueSessionInput { v := base; v.RequestID = "r\xff"; return v }(),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := IssueSession(ctx, badURL, input); !errors.Is(err, ErrInvalidSessionInput) {
				t.Fatalf("issue error = %v, want ErrInvalidSessionInput", err)
			}
		})
	}
	var verifier [32]byte
	if _, err := AuthenticateSession(ctx, badURL, verifier, time.Time{}); !errors.Is(err, ErrInvalidSessionInput) {
		t.Fatalf("zero-time authentication error = %v, want ErrInvalidSessionInput", err)
	}
	if _, err := RevokeSession(ctx, badURL, verifier, time.Time{}, "request"); !errors.Is(err, ErrInvalidSessionInput) {
		t.Fatalf("zero-time revoke error = %v, want ErrInvalidSessionInput", err)
	}
	if _, err := RevokeSession(ctx, badURL, verifier, time.Unix(1, 0), strings.Repeat("r", 129)); !errors.Is(err, ErrInvalidSessionInput) {
		t.Fatalf("oversized request revoke error = %v, want ErrInvalidSessionInput", err)
	}
}
