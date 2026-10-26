//go:build integration

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIntegrationAgentRotationSchedulesRetirementAndReplaysSafeHistory(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-agent", "rotate-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	firstAt := f.base.Add(5 * time.Second)
	input := RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, Overlap: new(time.Duration), CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{82}, IdempotencyKey: "rotate-zero", RequestID: "rotate-zero", Now: func() time.Time { return firstAt }}
	rotated, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || rotated.Replayed || rotated.Agent.Version != 2 || rotated.Credential.ID != input.CredentialID || rotated.PreviousCredential == nil || rotated.PreviousCredential.RetireAt == nil || !rotated.PreviousCredential.RetireAt.Equal(firstAt) {
		t.Fatalf("zero-overlap rotation = %+v, %v", rotated, err)
	}
	read, err := ListAgentCredentials(t.Context(), f.databaseURL, ListAgentCredentialsInput{SessionVerifier: f.verifier, AgentID: created.Agent.ID, Now: func() time.Time { return firstAt }})
	if err != nil || len(read.Items) != 2 || read.Items[0].RetireAt == nil || !read.Items[0].RetireAt.Equal(firstAt) {
		t.Fatalf("credential history after rotation = %+v, %v", read, err)
	}
	// Candidate material is deliberately excluded from the receipt digest and is never reused on replay.
	input.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdec"
	input.CredentialVerifier = [32]byte{83}
	input.Now = func() time.Time { return firstAt.Add(time.Second) }
	conn := integrationConnect(t, f.databaseURL)
	var receiptExpiry time.Time
	var auditCountBefore int
	if err := conn.QueryRow(t.Context(), "SELECT expires_at FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='agent.credential.rotate' AND idempotency_key=$2", f.userID, input.IdempotencyKey).Scan(&receiptExpiry); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id=$1", input.RequestID).Scan(&auditCountBefore); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	replay, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || !replay.Replayed || replay.Credential.ID != rotated.Credential.ID || replay.PreviousCredential == nil || replay.Agent != rotated.Agent {
		t.Fatalf("rotation replay = %+v, %v", replay, err)
	}
	conn = integrationConnect(t, f.databaseURL)
	var receiptExpiryAfter time.Time
	var auditCountAfter int
	if err := conn.QueryRow(t.Context(), "SELECT expires_at FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='agent.credential.rotate' AND idempotency_key=$2", f.userID, input.IdempotencyKey).Scan(&receiptExpiryAfter); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id=$1", input.RequestID).Scan(&auditCountAfter); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if !receiptExpiryAfter.Equal(receiptExpiry) || auditCountAfter != auditCountBefore {
		t.Fatalf("replay extended receipt/audit state expiry=%v->%v audits=%d->%d", receiptExpiry, receiptExpiryAfter, auditCountBefore, auditCountAfter)
	}
	var unused int
	conn = integrationConnect(t, f.databaseURL)
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE id=$1 OR verifier=$2", input.CredentialID, input.CredentialVerifier[:]).Scan(&unused); err != nil || unused != 0 {
		t.Fatalf("replay candidate rows=%d err=%v", unused, err)
	}
	var result []byte
	if err := conn.QueryRow(t.Context(), "SELECT result FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='agent.credential.rotate' AND idempotency_key=$2", f.userID, input.IdempotencyKey).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result, input.CredentialVerifier[:]) || bytes.Contains(result, []byte("verifier")) {
		t.Fatal("rotation receipt contains verifier material")
	}
	closeIntegrationConn(conn)
	changed := input
	different := time.Second
	changed.Overlap = &different
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, changed); !errors.Is(err, ErrAgentConflict) || got != (AgentRotationResult{}) {
		t.Fatalf("changed overlap replay result=%+v err=%v", got, err)
	}
	changed = input
	changed.Lifetime = time.Hour
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, changed); !errors.Is(err, ErrAgentConflict) || got != (AgentRotationResult{}) {
		t.Fatalf("changed lifetime replay result=%+v err=%v", got, err)
	}
	changed = input
	changed.ExpectedVersion = 2
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, changed); !errors.Is(err, ErrAgentConflict) || got != (AgentRotationResult{}) {
		t.Fatalf("changed version replay result=%+v err=%v", got, err)
	}
	revoked, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: rotated.PreviousCredential.ID, ExpectedVersion: 2, IdempotencyKey: "rotate-revoke-retired", RequestID: "rotate-revoke-retired", Now: func() time.Time { return firstAt.Add(2 * time.Second) }})
	if err != nil || revoked.Credential == nil || revoked.Credential.RetireAt == nil || !revoked.Credential.RetireAt.Equal(firstAt) {
		t.Fatalf("revoking retired credential = %+v, %v", revoked, err)
	}
	if _, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 3, IdempotencyKey: "rotate-disable-after-revoke", RequestID: "rotate-disable-after-revoke", Now: func() time.Time { return firstAt.Add(3 * time.Second) }}); err != nil {
		t.Fatal(err)
	}
	input.Now = func() time.Time { return firstAt.Add(4 * time.Second) }
	historical, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || !historical.Replayed || historical.Agent != rotated.Agent || historical.PreviousCredential == nil || historical.PreviousCredential.RevokedAt != nil {
		t.Fatalf("historical rotation replay after revoke/disable = %+v, %v", historical, err)
	}
}

func TestIntegrationAgentRotationRejectsTwoActiveAndRecoversAfterExplicitRevocation(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-active-agent", "rotate-active-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	rotate := func(version int64, key, id string, at time.Time, overlap time.Duration) AgentRotationResult {
		t.Helper()
		result, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: version, Overlap: &overlap, CredentialID: id, CredentialVerifier: [32]byte{byte(version + 90)}, IdempotencyKey: key, RequestID: key, Now: func() time.Time { return at }})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := rotate(1, "rotate-active-one", "abcdefab-cdef-4abc-8def-abcdefabcdeb", f.base.Add(5*time.Second), 10*time.Minute)
	secondInput := RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 2, Overlap: new(time.Duration), CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdec", CredentialVerifier: [32]byte{92}, IdempotencyKey: "rotate-active-two", RequestID: "rotate-active-two", Now: func() time.Time { return f.base.Add(6 * time.Second) }}
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, secondInput); !errors.Is(err, ErrAgentConflict) || got != (AgentRotationResult{}) {
		t.Fatalf("two-active rotation result=%+v err=%v", got, err)
	}
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, CredentialID: first.Credential.ID, ExpectedVersion: 2, IdempotencyKey: "rotate-revoke-active", RequestID: "rotate-revoke-active", Now: func() time.Time { return f.base.Add(6 * time.Second) }}); err != nil {
		t.Fatal(err)
	}
	secondInput.ExpectedVersion = 3
	secondInput.Now = func() time.Time { return f.base.Add(7 * time.Second) }
	second, err := RotateAgentCredential(t.Context(), f.databaseURL, secondInput)
	if err != nil || second.Agent.Version != 4 {
		t.Fatalf("rotation after explicit revoke = %+v, %v", second, err)
	}
}

func TestIntegrationAgentRotationUsesMinimumExistingCutoffAndExactExpiry(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-cutoff-agent", "rotate-cutoff-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	conn := integrationConnect(t, f.databaseURL)
	shortExpiry := f.base.Add(12 * time.Second)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agent_credentials SET expires_at=$2, retire_at=$3 WHERE id=$1", created.Credential.ID, shortExpiry, f.base.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	rotate := func(version int64, key, id string, at time.Time, overlap *time.Duration) AgentRotationResult {
		t.Helper()
		got, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: version, Overlap: overlap, CredentialID: id, CredentialVerifier: [32]byte{byte(version + 100)}, IdempotencyKey: key, RequestID: key, Now: func() time.Time { return at }})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	firstAt := f.base.Add(5 * time.Second)
	first := rotate(1, "rotate-cutoff-first", "abcdefab-cdef-4abc-8def-abcdefabcdeb", firstAt, nil)
	if first.PreviousCredential == nil || first.PreviousCredential.RetireAt == nil || !first.PreviousCredential.RetireAt.Equal(f.base.Add(8*time.Second)) {
		t.Fatalf("existing cutoff was extended: %+v", first.PreviousCredential)
	}
	// At exact retire_at equality, the historical credential has become inactive.
	secondAt := f.base.Add(8 * time.Second)
	second := rotate(2, "rotate-cutoff-equality", "abcdefab-cdef-4abc-8def-abcdefabcdec", secondAt, new(time.Duration))
	if second.PreviousCredential == nil || second.PreviousCredential.ID != first.Credential.ID || second.Agent.Version != 3 {
		t.Fatalf("exact-cutoff rotation = %+v", second)
	}
	short := f.input("rotate-expiry-agent", "rotate-expiry-create")
	short.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdee"
	shortCreated, err := CreateAgent(t.Context(), f.databaseURL, short)
	if err != nil {
		t.Fatal(err)
	}
	conn = integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agent_credentials SET expires_at=$2 WHERE id=$1", shortCreated.Credential.ID, f.base.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	expiryBound, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{Proof: proof, AgentID: shortCreated.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdf0", CredentialVerifier: [32]byte{95}, IdempotencyKey: "rotate-expiry-bound", RequestID: "rotate-expiry-bound", Now: func() time.Time { return firstAt }})
	if err != nil || expiryBound.PreviousCredential == nil || expiryBound.PreviousCredential.RetireAt == nil || !expiryBound.PreviousCredential.RetireAt.Equal(f.base.Add(12*time.Second)) {
		t.Fatalf("expiry-bounded cutoff = %+v, %v", expiryBound, err)
	}
	exactExpiry, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{Proof: proof, AgentID: shortCreated.Agent.ID, ExpectedVersion: 2, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdf2", CredentialVerifier: [32]byte{96}, IdempotencyKey: "rotate-expiry-equality", RequestID: "rotate-expiry-equality", Now: func() time.Time { return shortExpiry }})
	if err != nil || exactExpiry.Agent.Version != 3 || exactExpiry.PreviousCredential == nil || exactExpiry.PreviousCredential.ID != expiryBound.Credential.ID {
		t.Fatalf("rotation at exact expires_at = %+v, %v", exactExpiry, err)
	}
}

func TestIntegrationAgentRotationCandidateCollisionEpochRecoveryAndDisabledTargets(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-epoch-agent", "rotate-epoch-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	conn := integrationConnect(t, f.databaseURL)
	oldEpoch := "77777777-7777-4777-8777-777777777774"
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agent_credentials SET credential_epoch=$2 WHERE id=$1", created.Credential.ID, oldEpoch); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	at := f.base.Add(5 * time.Second)
	input := RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: created.Credential.ID, CredentialVerifier: [32]byte{94}, IdempotencyKey: "rotate-id-collision", RequestID: "rotate-id-collision", Now: func() time.Time { return at }}
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, input); !errors.Is(err, ErrAgentConflict) || got != (AgentRotationResult{}) {
		t.Fatalf("candidate collision result=%+v err=%v", got, err)
	}
	input.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdeb"
	input.IdempotencyKey = "rotate-epoch-recovery"
	input.RequestID = input.IdempotencyKey
	recovered, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || recovered.Agent.Version != 2 || recovered.PreviousCredential != nil || recovered.Credential.ID != input.CredentialID {
		t.Fatalf("old-epoch recovery = %+v, %v", recovered, err)
	}
	if _, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 2, IdempotencyKey: "rotate-disable", RequestID: "rotate-disable", Now: func() time.Time { return at.Add(time.Second) }}); err != nil {
		t.Fatal(err)
	}
	input.ExpectedVersion = 3
	input.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdec"
	input.IdempotencyKey = "rotate-disabled"
	input.RequestID = input.IdempotencyKey
	input.Now = func() time.Time { return at.Add(2 * time.Second) }
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, input); !errors.Is(err, ErrAgentConflict) || got != (AgentRotationResult{}) {
		t.Fatalf("disabled target result=%+v err=%v", got, err)
	}
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{Proof: proof, AgentID: "abcdefab-cdef-4abc-8def-abcdefabcdf1", ExpectedVersion: 1, CredentialID: input.CredentialID, IdempotencyKey: "rotate-missing", RequestID: "rotate-missing", Now: func() time.Time { return at.Add(3 * time.Second) }}); !errors.Is(err, ErrAgentNotFound) || got != (AgentRotationResult{}) {
		t.Fatalf("missing target result=%+v err=%v", got, err)
	}
}

func TestIntegrationAgentRotationMaxVersionConflictsWithoutOverflow(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-max-version-agent", "rotate-max-version-create"))
	if err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agents SET version=$2 WHERE id=$1", created.Agent.ID, int64(^uint64(0)>>1)); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	proof := f.input("unused", "unused").Proof
	got, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: int64(^uint64(0) >> 1), CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{96}, IdempotencyKey: "rotate-max-version", RequestID: "rotate-max-version", Now: func() time.Time { return f.base.Add(5 * time.Second) }})
	if !errors.Is(err, ErrAgentConflict) || got != (AgentRotationResult{}) {
		t.Fatalf("max-version rotation result=%+v err=%v", got, err)
	}
}

func TestIntegrationAgentRotationRejectsClockRegressionAfterAgentLock(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-clock-regression-agent", "rotate-clock-regression-create"))
	if err != nil {
		t.Fatal(err)
	}
	times := []time.Time{f.base.Add(20 * time.Second), f.base.Add(10 * time.Second)}
	var sample atomic.Int64
	clock := func() time.Time {
		index := sample.Add(1) - 1
		if index >= int64(len(times)) {
			index = int64(len(times) - 1)
		}
		return times[index]
	}
	proof := f.input("unused", "unused").Proof
	proof.VerifiedAt = f.base.Add(time.Second)
	input := RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{121}, IdempotencyKey: "rotate-clock-regression", RequestID: "rotate-clock-regression", Now: clock}
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, input); !errors.Is(err, ErrInvalidAgentInput) || got != (AgentRotationResult{}) {
		t.Fatalf("clock-regression result=%+v err=%v", got, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var version int64
	var credentials, receipts, audits int
	var retireAt *time.Time
	if err := conn.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE agent_id=$1", created.Agent.ID).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT retire_at FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&retireAt); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key=$1", input.IdempotencyKey).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id=$1", input.RequestID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if version != 1 || credentials != 1 || retireAt != nil || receipts != 0 || audits != 0 {
		t.Fatalf("clock regression changed version=%d credentials=%d retire_at=%v receipts=%d audits=%d", version, credentials, retireAt, receipts, audits)
	}
}

func TestIntegrationAgentRotationDefaultAndExplicitOverlapReplayEquivalently(t *testing.T) {
	f := newAgentFixture(t)
	creation := f.input("rotate-default-agent", "rotate-default-create")
	creation.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdea"
	created, err := CreateAgent(t.Context(), f.databaseURL, creation)
	if err != nil {
		t.Fatal(err)
	}
	proof := creation.Proof
	at := f.base.Add(5 * time.Second)
	input := RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{97}, IdempotencyKey: "rotate-default-equivalent", RequestID: "rotate-default-first", Now: func() time.Time { return at }}
	first, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || !first.Credential.ExpiresAt.Equal(at.Add(agentDefaultLifetime)) || first.PreviousCredential == nil || first.PreviousCredential.RetireAt == nil || !first.PreviousCredential.RetireAt.Equal(at.Add(10*time.Minute)) {
		t.Fatalf("default rotation = %+v, %v", first, err)
	}
	explicit := 10 * time.Minute
	input.Lifetime = agentDefaultLifetime
	input.Overlap = &explicit
	input.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdec"
	input.CredentialVerifier = [32]byte{98}
	input.Now = func() time.Time { return at.Add(time.Second) }
	replay, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || !replay.Replayed || replay.Credential.ID != first.Credential.ID {
		t.Fatalf("explicit equivalent replay = %+v, %v", replay, err)
	}
}

func TestIntegrationAgentRotationReceiptExpiresAtExact24HoursWithFreshSession(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-expired-receipt-agent", "rotate-expired-receipt-create"))
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	at := f.base.Add(5 * time.Second)
	input := RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{97}, IdempotencyKey: "rotate-expired-receipt", RequestID: "rotate-expired-first", Now: func() time.Time { return at }}
	first, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || first.Replayed {
		t.Fatalf("first rotation = %+v, %v", first, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var expires time.Time
	if err := conn.QueryRow(t.Context(), "SELECT expires_at FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='agent.credential.rotate' AND idempotency_key=$2", f.userID, input.IdempotencyKey).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if !expires.Equal(at.Add(24 * time.Hour)) {
		t.Fatalf("receipt expiry=%v want=%v", expires, at.Add(24*time.Hour))
	}
	var verifier [32]byte
	verifier[0] = 122
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision, Verifier: verifier, Now: expires, RequestID: "rotate-expired-receipt-session"}); err != nil {
		t.Fatal(err)
	}
	input.Proof = AgentAdminProof{SessionVerifier: verifier, UserID: f.userID, CredentialEpoch: f.epoch, CredentialRevision: f.revision, VerifiedAt: expires}
	input.ExpectedVersion = 2
	input.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdec"
	input.CredentialVerifier = [32]byte{98}
	input.RequestID = "rotate-expired-second"
	input.Now = func() time.Time { return expires }
	second, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
	if err != nil || second.Replayed || second.Agent.Version != 3 || second.Credential.ID != input.CredentialID {
		t.Fatalf("exact-expiry command = %+v, %v", second, err)
	}
	conn = integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var createdAt, expiresAt time.Time
	if err := conn.QueryRow(t.Context(), "SELECT created_at,expires_at FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='agent.credential.rotate' AND idempotency_key=$2", f.userID, input.IdempotencyKey).Scan(&createdAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if !createdAt.Equal(expires) || !expiresAt.Equal(expires.Add(24*time.Hour)) {
		t.Fatalf("fresh receipt bounds created=%v expires=%v exact=%v", createdAt, expiresAt, expires)
	}
}

func TestIntegrationAgentRotationReceiptIsScopedByActorAndOperationAndRechecksAuthority(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-isolation-agent", "rotate-isolation-create"))
	if err != nil {
		t.Fatal(err)
	}
	firstProof := f.input("unused", "unused").Proof
	key := "rotate-actor-operation-isolation"
	at := f.base.Add(5 * time.Second)
	firstInput := RotateAgentCredentialInput{Proof: firstProof, AgentID: created.Agent.ID, ExpectedVersion: 1, Overlap: new(time.Duration), CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{127}, IdempotencyKey: key, RequestID: "rotate-isolation-first", Now: func() time.Time { return at }}
	first, err := RotateAgentCredential(t.Context(), f.databaseURL, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	// A receipt under agent.create with the same actor/key is independent of rotation.
	createInput := f.input("rotate-isolation-second-agent", key)
	createInput.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdf1"
	if _, err := CreateAgent(t.Context(), f.databaseURL, createInput); err != nil {
		t.Fatalf("cross-operation create conflicted with rotation receipt: %v", err)
	}
	secondProof := addRotationAdministrator(t, f, "rotation-isolation-admin", 128)
	secondInput := RotateAgentCredentialInput{Proof: secondProof, AgentID: created.Agent.ID, ExpectedVersion: 2, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdf0", CredentialVerifier: [32]byte{129}, IdempotencyKey: key, RequestID: "rotate-isolation-second-actor", Now: func() time.Time { return at.Add(time.Second) }}
	second, err := RotateAgentCredential(t.Context(), f.databaseURL, secondInput)
	if err != nil || second.Replayed || second.Agent.Version != 3 {
		t.Fatalf("second actor same operation/key = %+v, %v", second, err)
	}
	firstInput.Now = func() time.Time { return at.Add(2 * time.Second) }
	replay, err := RotateAgentCredential(t.Context(), f.databaseURL, firstInput)
	if err != nil || !replay.Replayed || replay.Agent != first.Agent {
		t.Fatalf("historical actor-scoped replay = %+v, %v", replay, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET role='viewer' WHERE id=$1", f.userID); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if got, err := RotateAgentCredential(t.Context(), f.databaseURL, firstInput); !errors.Is(err, ErrAgentForbidden) || got != (AgentRotationResult{}) {
		t.Fatalf("replay after authority loss = %+v, %v", got, err)
	}
}

func TestIntegrationAgentRotationSerializesDifferentAdministratorsAtSharedVersion(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-admin-race-agent", "rotate-admin-race-create"))
	if err != nil {
		t.Fatal(err)
	}
	firstProof := f.input("unused", "unused").Proof
	secondProof := addRotationAdministrator(t, f, "second-rotation-admin", 112)
	secondProof.VerifiedAt = firstProof.VerifiedAt
	at := f.base.Add(5 * time.Second)
	inputs := []RotateAgentCredentialInput{
		{Proof: firstProof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{113}, IdempotencyKey: "admin-race-one", RequestID: "admin-race-one", Now: func() time.Time { return at }},
		{Proof: secondProof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdec", CredentialVerifier: [32]byte{114}, IdempotencyKey: "admin-race-two", RequestID: "admin-race-two", Now: func() time.Time { return at }},
	}
	type outcome struct {
		result AgentRotationResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, input := range inputs {
		wg.Go(func() {
			<-start
			result, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
			results <- outcome{result, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var successes, conflicts int
	for result := range results {
		if result.err == nil && result.result.Agent.Version == 2 {
			successes++
		} else if errors.Is(result.err, ErrAgentConflict) && result.result == (AgentRotationResult{}) {
			conflicts++
		} else {
			t.Fatalf("rotation race outcome=%+v err=%v", result.result, result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("rotation race successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestIntegrationAgentRotationRechecksFreshProofAndSessionAfterTargetLockWait(t *testing.T) {
	for _, tc := range []struct{ name, lock, pattern string }{
		{"agent lock", "SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE", "%FROM public.almanac_agents%"},
		{"credential lock", "SELECT id FROM public.almanac_agent_credentials WHERE id=$1 FOR UPDATE", "%FROM public.almanac_agent_credentials%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFixture(t)
			created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-wait-"+strings.ReplaceAll(tc.name, " ", "-"), "rotate-wait-create-"+strings.ReplaceAll(tc.name, " ", "-")))
			if err != nil {
				t.Fatal(err)
			}
			blocker := integrationConnect(t, f.databaseURL)
			tx, err := blocker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			key := created.Agent.ID
			if tc.name == "credential lock" {
				key = created.Credential.ID
			}
			if _, err := tx.Exec(t.Context(), tc.lock, key); err != nil {
				t.Fatal(err)
			}
			observer := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(observer)
			var lastSeen time.Time
			if err := observer.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", f.verifier[:]).Scan(&lastSeen); err != nil {
				t.Fatal(err)
			}
			initial := lastSeen.Add(sessionIdleLifetime - 2*time.Second)
			var nanos atomic.Int64
			nanos.Store(initial.UnixNano())
			proof := f.input("unused", "unused").Proof
			proof.VerifiedAt = initial.Add(-time.Second)
			input := RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{119}, IdempotencyKey: "rotate-wait-" + strings.ReplaceAll(tc.name, " ", "-"), RequestID: "rotate-wait-" + strings.ReplaceAll(tc.name, " ", "-"), Now: func() time.Time { return time.Unix(0, nanos.Load()).UTC() }}
			result := make(chan error, 1)
			go func() { _, err := RotateAgentCredential(t.Context(), f.databaseURL, input); result <- err }()
			waitForAgentQueryWait(t, observer, tc.pattern)
			nanos.Store(lastSeen.Add(sessionIdleLifetime).UnixNano())
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("post-lock expiry error=%v", err)
			}
			var version int64
			var credentials, audits, receipts int
			if err := observer.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE agent_id=$1", created.Agent.ID).Scan(&credentials); err != nil {
				t.Fatal(err)
			}
			if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id=$1", input.RequestID).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key=$1", input.IdempotencyKey).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if version != 1 || credentials != 1 || audits != 0 || receipts != 0 {
				t.Fatalf("lock-wait expiry mutated version=%d credentials=%d audits=%d receipts=%d", version, credentials, audits, receipts)
			}
			closeIntegrationConn(blocker)
		})
	}
}

func TestIntegrationAgentRotationRejectsProofAtExactExpiryWhileSessionRemainsLive(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-proof-expiry-agent", "rotate-proof-expiry-create"))
	if err != nil {
		t.Fatal(err)
	}
	blocker := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(blocker)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), "SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE", created.Agent.ID); err != nil {
		t.Fatal(err)
	}
	observer := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(observer)
	initial := f.base.Add(5 * time.Second)
	verified := initial.Add(-time.Second)
	var nanos atomic.Int64
	nanos.Store(initial.UnixNano())
	input := RotateAgentCredentialInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{130}, IdempotencyKey: "rotate-proof-expiry", RequestID: "rotate-proof-expiry", Now: func() time.Time { return time.Unix(0, nanos.Load()).UTC() }}
	input.Proof.VerifiedAt = verified
	result := make(chan error, 1)
	go func() { _, err := RotateAgentCredential(t.Context(), f.databaseURL, input); result <- err }()
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_agents%")
	nanos.Store(verified.Add(30 * time.Second).UnixNano())
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("exact proof-expiry error=%v", err)
	}
	var version int64
	var credentials, receipts, audits int
	var retireAt *time.Time
	if err := observer.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE agent_id=$1", created.Agent.ID).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(t.Context(), "SELECT retire_at FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&retireAt); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key=$1", input.IdempotencyKey).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id=$1", input.RequestID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if version != 1 || credentials != 1 || retireAt != nil || receipts != 0 || audits != 0 {
		t.Fatalf("proof-expiry denial mutated version=%d credentials=%d retire=%v receipts=%d audits=%d", version, credentials, retireAt, receipts, audits)
	}
}

func TestIntegrationAgentRotationCancellationDuringTargetLockLeavesNoMutation(t *testing.T) {
	f := newAgentFixture(t)
	created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-cancel-agent", "rotate-cancel-create"))
	if err != nil {
		t.Fatal(err)
	}
	blocker := integrationConnect(t, f.databaseURL)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), "SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE", created.Agent.ID); err != nil {
		t.Fatal(err)
	}
	observer := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(observer)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := RotateAgentCredentialInput{Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{120}, IdempotencyKey: "rotate-cancel", RequestID: "rotate-cancel", Now: func() time.Time { return f.base.Add(5 * time.Second) }}
	result := make(chan error, 1)
	go func() { _, err := RotateAgentCredential(ctx, f.databaseURL, input); result <- err }()
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_agents%")
	cancel()
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancelled rotation error=%v", err)
	}
	var version int64
	var credentials, audits, receipts int
	if err := observer.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE agent_id=$1", created.Agent.ID).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id='rotate-cancel'").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key='rotate-cancel'").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if version != 1 || credentials != 1 || audits != 0 || receipts != 0 {
		t.Fatalf("cancelled rotation mutated version=%d credentials=%d audits=%d receipts=%d", version, credentials, audits, receipts)
	}
	closeIntegrationConn(blocker)
}

func TestIntegrationAgentRotationOrdersAgainstDisableAndRevoke(t *testing.T) {
	for _, tc := range []struct {
		name        string
		firstAction string
	}{
		{"rotation before disable", "agent.credential.rotated"},
		{"disable before rotation", "agent.disabled"},
		{"revoke before rotation", "agent.credential.revoked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFixture(t)
			created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-order-"+strings.ReplaceAll(tc.name, " ", "-"), "rotate-order-create-"+strings.ReplaceAll(tc.name, " ", "-")))
			if err != nil {
				t.Fatal(err)
			}
			firstProof := f.input("unused", "unused").Proof
			secondProof := addRotationAdministrator(t, f, "second-order-admin", 124)
			secondProof.VerifiedAt = firstProof.VerifiedAt
			lockKey := int64(1770141007)
			locker := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(locker)
			if _, err := locker.Exec(t.Context(), "SELECT pg_advisory_lock($1)", lockKey); err != nil {
				t.Fatal(err)
			}
			observer := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(observer)
			triggerName := "block_rotation_order"
			functionName := triggerName
			statement := fmt.Sprintf("CREATE FUNCTION public.%s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW WHEN (NEW.action='%s') EXECUTE FUNCTION public.%s();", functionName, lockKey, triggerName, tc.firstAction, functionName)
			if _, err := observer.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			firstAt := f.base.Add(5 * time.Second)
			firstDone := make(chan error, 1)
			if tc.firstAction == "agent.credential.rotated" {
				go func() {
					_, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{Proof: firstProof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{125}, IdempotencyKey: "order-rotation", RequestID: "order-rotation", Now: func() time.Time { return firstAt }})
					firstDone <- err
				}()
			} else if tc.firstAction == "agent.disabled" {
				go func() {
					_, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: firstProof, AgentID: created.Agent.ID, ExpectedVersion: 1, IdempotencyKey: "order-disable", RequestID: "order-disable", Now: func() time.Time { return firstAt }})
					firstDone <- err
				}()
			} else {
				go func() {
					_, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{Proof: firstProof, AgentID: created.Agent.ID, CredentialID: created.Credential.ID, ExpectedVersion: 1, IdempotencyKey: "order-revoke", RequestID: "order-revoke", Now: func() time.Time { return firstAt }})
					firstDone <- err
				}()
			}
			waitForAgentQueryWait(t, observer, "%INSERT INTO public.almanac_audit_events%")
			secondAt := f.base.Add(6 * time.Second)
			secondDone := make(chan struct {
				rotation  AgentRotationResult
				lifecycle AgentLifecycleResult
				err       error
			}, 1)
			if tc.firstAction == "agent.credential.rotated" {
				go func() {
					result, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{Proof: secondProof, AgentID: created.Agent.ID, ExpectedVersion: 2, IdempotencyKey: "order-disable-after-rotation", RequestID: "order-disable-after-rotation", Now: func() time.Time { return secondAt }})
					secondDone <- struct {
						rotation  AgentRotationResult
						lifecycle AgentLifecycleResult
						err       error
					}{lifecycle: result, err: err}
				}()
			} else {
				go func() {
					result, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{Proof: secondProof, AgentID: created.Agent.ID, ExpectedVersion: 2, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdec", CredentialVerifier: [32]byte{126}, IdempotencyKey: "order-rotation-after", RequestID: "order-rotation-after", Now: func() time.Time { return secondAt }})
					secondDone <- struct {
						rotation  AgentRotationResult
						lifecycle AgentLifecycleResult
						err       error
					}{rotation: result, err: err}
				}()
			}
			waitForAgentQueryWait(t, observer, "%FROM public.almanac_agents%")
			if _, err := locker.Exec(t.Context(), "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
				t.Fatal(err)
			}
			if err := <-firstDone; err != nil {
				t.Fatalf("first operation error=%v", err)
			}
			second := <-secondDone
			if tc.firstAction == "agent.credential.rotated" {
				if second.err != nil || !second.lifecycle.Changed || second.lifecycle.Agent.Enabled || second.lifecycle.RevokedCredentialCount != 2 {
					t.Fatalf("disable after rotation = %+v, %v", second.lifecycle, second.err)
				}
				var unrevoked int
				if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE agent_id=$1 AND revoked_at IS NULL", created.Agent.ID).Scan(&unrevoked); err != nil || unrevoked != 0 {
					t.Fatalf("disable left %d unrevoked credentials: %v", unrevoked, err)
				}
			} else if tc.firstAction == "agent.disabled" {
				if !errors.Is(second.err, ErrAgentConflict) || second.rotation != (AgentRotationResult{}) {
					t.Fatalf("rotation after disable = %+v, %v", second.rotation, second.err)
				}
				var revoked time.Time
				if err := observer.QueryRow(t.Context(), "SELECT revoked_at FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&revoked); err != nil || revoked.IsZero() {
					t.Fatalf("disable did not revoke original credential: %v", err)
				}
			} else {
				if second.err != nil || second.rotation.Agent.Version != 3 || second.rotation.PreviousCredential != nil {
					t.Fatalf("rotation after revoke = %+v, %v", second.rotation, second.err)
				}
				var revoked time.Time
				if err := observer.QueryRow(t.Context(), "SELECT revoked_at FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&revoked); err != nil || !revoked.Equal(firstAt) {
					t.Fatalf("rotation changed explicit revocation timestamp: %v %v", revoked, err)
				}
			}
			if _, err := observer.Exec(t.Context(), "DROP TRIGGER "+triggerName+" ON public.almanac_audit_events"); err != nil {
				t.Fatal(err)
			}
			if _, err := observer.Exec(t.Context(), "DROP FUNCTION public."+functionName+"()"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIntegrationAgentRotationAuditAndReceiptFailuresRollbackRetirement(t *testing.T) {
	for _, failure := range []string{"audit", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			f := newAgentFixture(t)
			created, err := CreateAgent(t.Context(), f.databaseURL, f.input("rotate-rollback-"+failure, "rotate-rollback-create-"+failure))
			if err != nil {
				t.Fatal(err)
			}
			conn := integrationConnect(t, f.databaseURL)
			trigger := "fail_rotate_" + failure
			function := trigger
			table := "almanac_audit_events"
			condition := " WHEN (NEW.action='agent.credential.rotated')"
			if failure == "receipt" {
				table = "almanac_management_receipts"
				condition = " WHEN (NEW.operation='agent.credential.rotate')"
			}
			statement := fmt.Sprintf("CREATE FUNCTION public.%s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'rotation fixture failure'; END $$; CREATE TRIGGER %s BEFORE INSERT ON public.%s FOR EACH ROW%s EXECUTE FUNCTION public.%s();", function, trigger, table, condition, function)
			if _, err := conn.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			closeIntegrationConn(conn)
			proof := f.input("unused", "unused").Proof
			input := RotateAgentCredentialInput{Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdeb", CredentialVerifier: [32]byte{99}, IdempotencyKey: "rotate-rollback-" + failure, RequestID: "rotate-rollback-" + failure, Now: func() time.Time { return f.base.Add(5 * time.Second) }}
			got, err := RotateAgentCredential(t.Context(), f.databaseURL, input)
			want := map[string]string{"audit": "storage: agent rotation audit failed", "receipt": "storage: agent rotation receipt insert failed"}[failure]
			if err == nil || err.Error() != want || got != (AgentRotationResult{}) {
				t.Fatalf("failure result=%+v err=%v want=%q", got, err, want)
			}
			conn = integrationConnect(t, f.databaseURL)
			var version int64
			var credentialCount, auditCount, receiptCount int
			if err := conn.QueryRow(t.Context(), "SELECT version FROM public.almanac_agents WHERE id=$1", created.Agent.ID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE agent_id=$1", created.Agent.ID).Scan(&credentialCount); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id=$1", input.RequestID).Scan(&auditCount); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE operation='agent.credential.rotate' AND idempotency_key=$1", input.IdempotencyKey).Scan(&receiptCount); err != nil {
				t.Fatal(err)
			}
			var retireAt *time.Time
			if err := conn.QueryRow(t.Context(), "SELECT retire_at FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&retireAt); err != nil {
				t.Fatal(err)
			}
			if version != 1 || credentialCount != 1 || auditCount != 0 || receiptCount != 0 || retireAt != nil {
				t.Fatalf("rollback state version=%d credentials=%d audits=%d receipts=%d retire_at=%v", version, credentialCount, auditCount, receiptCount, retireAt)
			}
			if _, err := conn.Exec(t.Context(), "DROP TRIGGER "+trigger+" ON public."+table); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(t.Context(), "DROP FUNCTION public."+function+"()"); err != nil {
				t.Fatal(err)
			}
			closeIntegrationConn(conn)
		})
	}
}

func TestIntegrationRetirementMigrationPreservesExistingAgentAndIdentityRows(t *testing.T) {
	databaseURL := integrationDatabase(t)
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:5]); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	const userID = "77777777-7777-4777-8777-777777777771"
	const agentID = "77777777-7777-4777-8777-777777777772"
	const credentialID = "77777777-7777-4777-8777-777777777773"
	const sessionVerifier = "0101010101010101010101010101010101010101010101010101010101010101"
	created := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var epoch string
	if err := conn.QueryRow(t.Context(), "SELECT credential_epoch::text FROM public.almanac_installation WHERE singleton").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_users(id,login,password_verifier,role,enabled,must_change_password,credential_revision,created_at,updated_at) VALUES($1,'migration-admin',$2,'administrator',true,false,1,$3,$3)`, userID, testPasswordVerifier, created); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET bootstrap_admin_id=$1,bootstrapped_at=$2,onboarding_completed_at=$2 WHERE singleton", userID, created); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_sessions(user_id,verifier,credential_revision,credential_epoch,restricted,created_at,last_seen_at) VALUES($1,decode($2,'hex'),1,$3,false,$4,$4)`, userID, sessionVerifier, epoch, created); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agents(id,name,display_name,enabled,version,created_at,updated_at) VALUES($1,'migration-agent','Migration Agent',true,1,$2,$2)`, agentID, created); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at) VALUES($1,$2,decode(repeat('01',32),'hex'),$3,$4,$5)`, credentialID, agentID, epoch, created, created.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	receipt := []byte(`{"version":1,"agent":{"id":"77777777-7777-4777-8777-777777777772","name":"migration-agent","display_name":"Migration Agent","enabled":true,"version":1,"created_at":"2026-10-26T12:00:00Z","updated_at":"2026-10-26T12:00:00Z"},"credential":{"id":"77777777-7777-4777-8777-777777777773","agent_id":"77777777-7777-4777-8777-777777777772","issued_at":"2026-10-26T12:00:00Z","expires_at":"2026-10-26T13:00:00Z"},"changed":true,"revoked_credential_count":1}`)
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_management_receipts(actor_id,operation,idempotency_key,digest,result,created_at,expires_at) VALUES($1,'agent.credential.revoke','old-receipt',decode(repeat('01',32),'hex'),$2,$3,$4)`, userID, string(receipt), created, created.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if result, err := Upgrade(t.Context(), databaseURL); err != nil || result.Applied != 2 || result.DatabaseSchemaVersion != 7 {
		t.Fatalf("schema 5 to 7 upgrade = %+v, %v", result, err)
	}
	conn = integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var gotAgent, gotUser string
	var gotSession []byte
	var retireAt *time.Time
	var receiptPreserved bool
	if err := conn.QueryRow(t.Context(), "SELECT id::text FROM public.almanac_agents WHERE id=$1", agentID).Scan(&gotAgent); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT id::text FROM public.almanac_users WHERE id=$1", userID).Scan(&gotUser); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT verifier FROM public.almanac_sessions WHERE user_id=$1", userID).Scan(&gotSession); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT retire_at FROM public.almanac_agent_credentials WHERE id=$1", credentialID).Scan(&retireAt); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT result=$2::jsonb FROM public.almanac_management_receipts WHERE actor_id=$1 AND idempotency_key='old-receipt'", userID, string(receipt)).Scan(&receiptPreserved); err != nil {
		t.Fatal(err)
	}
	if gotAgent != agentID || gotUser != userID || !bytes.Equal(gotSession, bytes.Repeat([]byte{1}, 32)) || retireAt != nil || !receiptPreserved {
		t.Fatalf("migration changed prior state agent=%s user=%s session_len=%d retire=%v receipt_equal=%v", gotAgent, gotUser, len(gotSession), retireAt, receiptPreserved)
	}
}

func addRotationAdministrator(t *testing.T, fixture agentFixture, login string, marker byte) AgentAdminProof {
	t.Helper()
	conn := integrationConnect(t, fixture.databaseURL)
	var userID string
	if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_users(login,password_verifier,role,enabled,must_change_password,credential_revision,created_at,updated_at) VALUES($1,$2,'administrator',true,false,$3,$4,$4) RETURNING id::text`, login, testPasswordVerifier, fixture.revision, fixture.base).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = marker
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_sessions(user_id,verifier,credential_revision,credential_epoch,restricted,created_at,last_seen_at) VALUES($1,$2,$3,$4,false,$5,$5)`, userID, verifier[:], fixture.revision, fixture.epoch, fixture.base); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	return AgentAdminProof{SessionVerifier: verifier, UserID: userID, CredentialEpoch: fixture.epoch, CredentialRevision: fixture.revision, VerifiedAt: fixture.base.Add(2 * time.Second)}
}
