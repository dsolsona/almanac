package storage

import (
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestRotateAgentCredentialValidatesSemanticBoundsBeforeDatabaseAccess(t *testing.T) {
	base := validCreateAgentInput()
	valid := RotateAgentCredentialInput{Proof: base.Proof, AgentID: "abcdefab-cdef-4abc-8def-abcdefabcdee", ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{4}, IdempotencyKey: "rotate-key", RequestID: "rotate-request", Now: base.Now}
	for _, tc := range []struct {
		name string
		edit func(*RotateAgentCredentialInput)
	}{
		{"lifetime fractional", func(i *RotateAgentCredentialInput) { i.Lifetime = time.Second + time.Nanosecond }},
		{"lifetime over maximum", func(i *RotateAgentCredentialInput) { i.Lifetime = agentMaxLifetime + time.Second }},
		{"overlap fractional", func(i *RotateAgentCredentialInput) { v := time.Nanosecond; i.Overlap = &v }},
		{"overlap over ten minutes", func(i *RotateAgentCredentialInput) { v := 10*time.Minute + time.Second; i.Overlap = &v }},
		{"negative overlap", func(i *RotateAgentCredentialInput) { v := -time.Second; i.Overlap = &v }},
		{"invalid target", func(i *RotateAgentCredentialInput) { i.AgentID = "bad" }},
		{"missing proof time", func(i *RotateAgentCredentialInput) { i.Proof.VerifiedAt = time.Time{} }},
		{"missing clock", func(i *RotateAgentCredentialInput) { i.Now = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.edit(&input)
			if _, err := RotateAgentCredential(t.Context(), "invalid database URL", input); !errors.Is(err, ErrInvalidAgentInput) {
				t.Fatalf("rotation validation error=%v", err)
			}
		})
	}
	valid.Lifetime = 0
	if _, err := RotateAgentCredential(t.Context(), "invalid database URL", valid); err == nil || errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("valid default-duration input did not pass validation: %v", err)
	}
	for _, tc := range []struct {
		name     string
		lifetime time.Duration
		overlap  time.Duration
	}{{"one second and zero overlap", time.Second, 0}, {"maximum lifetime and overlap", agentMaxLifetime, 10 * time.Minute}} {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			input.Lifetime = tc.lifetime
			input.Overlap = &tc.overlap
			if _, err := RotateAgentCredential(t.Context(), "invalid database URL", input); err == nil || errors.Is(err, ErrInvalidAgentInput) {
				t.Fatalf("accepted duration bounds did not reach database boundary: %v", err)
			}
		})
	}
}

func TestAgentRotationReceiptAllowsPresentNullStateAndRejectsMissingOrInvalidFields(t *testing.T) {
	issued := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	agentID := "abcdefab-cdef-4abc-8def-abcdefabcdee"
	saved := agentRotationReceipt{Version: 1, Agent: AgentRecord{ID: agentID, Name: "agent-one", DisplayName: "Agent One", Enabled: true, Version: 2, CreatedAt: issued, UpdatedAt: issued.Add(time.Second)}, Credential: AgentCredentialStateRecord{ID: "abcdefab-cdef-4abc-8def-abcdefabcdea", AgentID: agentID, IssuedAt: issued, ExpiresAt: issued.Add(time.Hour)}}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeAgentRotationReceipt(raw)
	if err != nil || !validAgentRotationReceipt(decoded, mustUUID(t, agentID), 1) {
		t.Fatalf("valid null-state receipt decoded as %+v, %v", decoded, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(string) string
	}{
		{"missing revoked_at", func(raw string) string { return strings.Replace(raw, `,"revoked_at":null`, "", 1) }},
		{"missing retire_at", func(raw string) string { return strings.Replace(raw, `,"retire_at":null`, "", 1) }},
		{"revoked replacement", func(raw string) string {
			return strings.Replace(raw, `"revoked_at":null`, `"revoked_at":"2026-10-26T10:00:01Z"`, 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeAgentRotationReceipt([]byte(tc.mutate(string(raw)))); err == nil {
				t.Fatal("decoder accepted corrupted credential state")
			}
		})
	}
	previous := saved.Credential
	previous.ID = "abcdefab-cdef-4abc-8def-abcdefabcdeb"
	retirement := issued.Add(10 * time.Minute)
	previous.RetireAt = &retirement
	saved.PreviousCredential = &previous
	raw, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = decodeAgentRotationReceipt(raw)
	if err != nil || !validAgentRotationReceipt(decoded, mustUUID(t, agentID), 1) || decoded.PreviousCredential == nil || decoded.PreviousCredential.RetireAt == nil {
		t.Fatalf("valid previous retirement receipt decoded as %+v, previous=%+v raw=%s error=%v", decoded, decoded.PreviousCredential, raw, err)
	}
	withoutPreviousRetirement := strings.Replace(string(raw), `"retire_at":"2026-10-26T10:10:00Z"`, `"retire_at":null`, 1)
	corrupt, err := decodeAgentRotationReceipt([]byte(withoutPreviousRetirement))
	if err != nil || validAgentRotationReceipt(corrupt, mustUUID(t, agentID), 1) {
		t.Fatalf("receipt accepted previous credential with null retirement: %+v err=%v", corrupt, err)
	}
	previous.RetireAt = nil
	saved.PreviousCredential = &previous
	if validAgentRotationReceipt(saved, mustUUID(t, agentID), 1) {
		t.Fatal("rotation receipt accepted a previous credential without a retirement timestamp")
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, ok := canonicalUUID(value)
	if !ok {
		t.Fatal("test UUID is invalid")
	}
	return parsed
}
