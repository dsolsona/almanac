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

	"github.com/jackc/pgx/v5"
)

func TestIntegrationCreateAgentPersistsSafeReceiptAndReplaysMetadata(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "agent-admin", PasswordVerifier: testPasswordVerifier, Now: base, RequestID: "bootstrap-agent"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "agent-admin")
	if err != nil {
		t.Fatal(err)
	}
	var firstSession, rotatedSession [32]byte
	firstSession[0], rotatedSession[0] = 81, 82
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: firstSession, Now: base, RequestID: "issue-agent-admin",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
		CurrentSessionVerifier: firstSession, ExpectedCredentialRevision: identity.CredentialRevision,
		ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
		NewSessionVerifier: rotatedSession, Now: base.Add(time.Second), RequestID: "complete-agent-onboarding",
	}); err != nil {
		t.Fatal(err)
	}
	verifiedAt := base.Add(2 * time.Second)
	now := verifiedAt.Add(time.Second)
	input := CreateAgentInput{
		Proof: AgentAdminProof{SessionVerifier: rotatedSession, UserID: owner.State.AdministratorID,
			CredentialEpoch: identity.CredentialEpoch, CredentialRevision: identity.CredentialRevision + 1, VerifiedAt: verifiedAt},
		Name: "metrics-east", DisplayName: "Metrics East", CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdea",
		CredentialVerifier: [32]byte{83}, IdempotencyKey: "register-agent-1", RequestID: "create-agent-1",
		Now: func() time.Time { return now },
	}
	created, err := CreateAgent(t.Context(), databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Replayed || created.Agent.Name != input.Name || created.Agent.Version != 1 || created.Credential.AgentID != created.Agent.ID || !created.Credential.ExpiresAt.Equal(now.Add(agentDefaultLifetime)) {
		t.Fatalf("creation result = %+v", created)
	}
	originalVerifier := input.CredentialVerifier
	replayCandidateID := "abcdefab-cdef-4abc-8def-abcdefabcdeb"
	replayCandidateVerifier := [32]byte{84}
	input.CredentialID = replayCandidateID
	input.CredentialVerifier = replayCandidateVerifier
	input.Lifetime = agentDefaultLifetime
	replayed, err := CreateAgent(t.Context(), databaseURL, input)
	if err != nil || !replayed.Replayed || replayed.Agent != created.Agent || replayed.Credential != created.Credential {
		t.Fatalf("replayed result = %+v, %v", replayed, err)
	}
	conflicting := input
	conflicting.DisplayName = "Different name"
	if _, err := CreateAgent(t.Context(), databaseURL, conflicting); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("different receipt body error = %v; want conflict", err)
	}

	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var agents, credentials, receipts, audits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agents").Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials").Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action IN ('agent.created','agent.credential.issued')").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if agents != 1 || credentials != 1 || receipts != 1 || audits != 2 {
		t.Fatalf("rows agents=%d credentials=%d receipts=%d audits=%d; want 1,1,1,2", agents, credentials, receipts, audits)
	}
	var storedVerifier, receiptJSON []byte
	if err := conn.QueryRow(t.Context(), "SELECT verifier FROM public.almanac_agent_credentials WHERE id=$1", created.Credential.ID).Scan(&storedVerifier); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedVerifier, originalVerifier[:]) {
		t.Fatal("credential verifier did not persist as supplied")
	}
	var unusedCandidateCount int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE id=$1 OR verifier=$2", replayCandidateID, replayCandidateVerifier[:]).Scan(&unusedCandidateCount); err != nil {
		t.Fatal(err)
	}
	if unusedCandidateCount != 0 {
		t.Fatalf("unused replay candidate persisted in %d credential rows", unusedCandidateCount)
	}
	if err := conn.QueryRow(t.Context(), "SELECT result FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='agent.create' AND idempotency_key=$2", owner.State.AdministratorID, input.IdempotencyKey).Scan(&receiptJSON); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(receiptJSON, originalVerifier[:]) || bytes.Contains(receiptJSON, replayCandidateVerifier[:]) || bytes.Contains(receiptJSON, []byte("verifier")) || strings.Contains(string(receiptJSON), replayCandidateID) {
		t.Fatal("safe receipt contains credential verifier or candidate credential id")
	}
	var createdAt, expiresAt time.Time
	if err := conn.QueryRow(t.Context(), "SELECT created_at, expires_at FROM public.almanac_management_receipts WHERE actor_id=$1 AND operation='agent.create' AND idempotency_key=$2", owner.State.AdministratorID, input.IdempotencyKey).Scan(&createdAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if !expiresAt.Equal(createdAt.Add(agentCreateReceiptTTL)) {
		t.Fatalf("receipt expiry delta = %v; want exactly %v", expiresAt.Sub(createdAt), agentCreateReceiptTTL)
	}
}

type agentFixture struct {
	databaseURL string
	userID      string
	epoch       string
	revision    int64
	verifier    [32]byte
	base        time.Time
}

func newAgentFixture(t *testing.T) agentFixture {
	t.Helper()
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "agent-admin", PasswordVerifier: testPasswordVerifier, Now: base, RequestID: "bootstrap-agent"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "agent-admin")
	if err != nil {
		t.Fatal(err)
	}
	var firstSession, session [32]byte
	firstSession[0], session[0] = 81, 82
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: firstSession, Now: base, RequestID: "issue-agent-admin",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
		CurrentSessionVerifier: firstSession, ExpectedCredentialRevision: identity.CredentialRevision,
		ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
		NewSessionVerifier: session, Now: base.Add(time.Second), RequestID: "complete-agent-onboarding",
	}); err != nil {
		t.Fatal(err)
	}
	return agentFixture{databaseURL: databaseURL, userID: owner.State.AdministratorID, epoch: identity.CredentialEpoch, revision: identity.CredentialRevision + 1, verifier: session, base: base}
}

func (f agentFixture) input(name, key string) CreateAgentInput {
	verifiedAt := f.base.Add(2 * time.Second)
	return CreateAgentInput{
		Proof: AgentAdminProof{SessionVerifier: f.verifier, UserID: f.userID, CredentialEpoch: f.epoch, CredentialRevision: f.revision, VerifiedAt: verifiedAt},
		Name:  name, DisplayName: "Agent " + name, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdea",
		CredentialVerifier: [32]byte{91}, IdempotencyKey: key, RequestID: "request-" + key,
		Now: func() time.Time { return verifiedAt.Add(time.Second) },
	}
}

func TestIntegrationCreateAgentDenialsPreserveRegistrationRows(t *testing.T) {
	cases := []struct {
		name string
		edit func(*testing.T, *agentFixture, *CreateAgentInput)
		want error
	}{
		{"viewer", func(t *testing.T, f *agentFixture, _ *CreateAgentInput) { updateAgentUser(t, f, "role='viewer'") }, ErrAgentForbidden},
		{"editor", func(t *testing.T, f *agentFixture, _ *CreateAgentInput) { updateAgentUser(t, f, "role='editor'") }, ErrAgentForbidden},
		{"restricted session", func(t *testing.T, f *agentFixture, _ *CreateAgentInput) {
			conn := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(conn)
			if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_sessions SET restricted=true WHERE verifier=$1", f.verifier[:]); err != nil {
				t.Fatal(err)
			}
		}, ErrAgentForbidden},
		{"disabled user", func(t *testing.T, f *agentFixture, _ *CreateAgentInput) { updateAgentUser(t, f, "enabled=false") }, ErrInvalidSession},
		{"revoked session", func(t *testing.T, f *agentFixture, _ *CreateAgentInput) {
			if revoked, err := RevokeSession(t.Context(), f.databaseURL, f.verifier, f.base.Add(3*time.Second), "revoke-agent-session"); err != nil || !revoked {
				t.Fatalf("revoke session = %t, %v", revoked, err)
			}
		}, ErrInvalidSession},
		{"expired session", func(_ *testing.T, f *agentFixture, input *CreateAgentInput) {
			input.Proof.VerifiedAt = f.base.Add(13 * time.Hour)
			input.Now = func() time.Time { return f.base.Add(13*time.Hour + time.Second) }
		}, ErrInvalidSession},
		{"stale epoch", func(_ *testing.T, _ *agentFixture, input *CreateAgentInput) {
			input.Proof.CredentialEpoch = "abcdefab-cdef-4abc-8def-abcdefabcdee"
		}, ErrInvalidSession},
		{"stale revision", func(_ *testing.T, f *agentFixture, input *CreateAgentInput) {
			input.Proof.CredentialRevision = f.revision - 1
		}, ErrInvalidSession},
		{"wrong proof user", func(_ *testing.T, _ *agentFixture, input *CreateAgentInput) {
			input.Proof.UserID = "abcdefab-cdef-4abc-8def-abcdefabcdee"
		}, ErrInvalidSession},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newAgentFixture(t)
			input := f.input("denial-agent", "denial-key")
			test.edit(t, &f, &input)
			before := agentRowCounts(t, f.databaseURL)
			if _, err := CreateAgent(t.Context(), f.databaseURL, input); !errors.Is(err, test.want) {
				t.Fatalf("CreateAgent error = %v; want %v", err, test.want)
			}
			after := agentRowCounts(t, f.databaseURL)
			if after != before {
				t.Fatalf("denial changed registration rows: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestIntegrationCreateAgentConcurrentReceiptWriters(t *testing.T) {
	for _, conflictBody := range []bool{false, true} {
		name := "same-body"
		if conflictBody {
			name = "different-body"
		}
		t.Run(name, func(t *testing.T) {
			f := newAgentFixture(t)
			first := f.input("concurrent-agent", "concurrent-key")
			second := first
			second.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdeb"
			second.CredentialVerifier = [32]byte{92}
			if conflictBody {
				second.DisplayName = "Changed body"
			}
			start := make(chan struct{})
			type result struct {
				creation AgentCreation
				err      error
			}
			results := make(chan result, 2)
			var wg sync.WaitGroup
			wg.Go(func() { <-start; c, err := CreateAgent(t.Context(), f.databaseURL, first); results <- result{c, err} })
			wg.Go(func() { <-start; c, err := CreateAgent(t.Context(), f.databaseURL, second); results <- result{c, err} })
			close(start)
			a, b := <-results, <-results
			wg.Wait()
			if conflictBody {
				if (errors.Is(a.err, ErrAgentConflict) && b.err == nil) == false && (errors.Is(b.err, ErrAgentConflict) && a.err == nil) == false {
					t.Fatalf("different-body outcomes = (%+v,%v), (%+v,%v)", a.creation, a.err, b.creation, b.err)
				}
			} else {
				if a.err != nil || b.err != nil || a.creation.Agent != b.creation.Agent || a.creation.Credential != b.creation.Credential || a.creation.Replayed == b.creation.Replayed {
					t.Fatalf("same-body results = (%+v,%v), (%+v,%v)", a.creation, a.err, b.creation, b.err)
				}
			}
			if counts := agentRowCounts(t, f.databaseURL); counts.agents != 1 || counts.credentials != 1 || counts.receipts != 1 || counts.audits != 2 {
				t.Fatalf("concurrent result counts = %+v", counts)
			}
		})
	}
}

func TestIntegrationCreateAgentActorIsolationAndFreshSessionReplay(t *testing.T) {
	f := newAgentFixture(t)
	first := f.input("actor-one-agent", "shared-key")
	created, err := CreateAgent(t.Context(), f.databaseURL, first)
	if err != nil {
		t.Fatal(err)
	}
	var fresh [32]byte
	fresh[0] = 93
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision, Verifier: fresh, Now: f.base.Add(5 * time.Second), RequestID: "fresh-session"}); err != nil {
		t.Fatal(err)
	}
	replay := first
	replay.Proof.SessionVerifier = fresh
	replay.Proof.VerifiedAt = f.base.Add(6 * time.Second)
	replay.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdeb"
	replay.Now = func() time.Time { return f.base.Add(7 * time.Second) }
	replayed, err := CreateAgent(t.Context(), f.databaseURL, replay)
	if err != nil || !replayed.Replayed || replayed.Agent != created.Agent {
		t.Fatalf("fresh-session replay = %+v, %v", replayed, err)
	}

	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var otherID string
	if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_users (login,password_verifier,role,enabled,must_change_password,created_at,updated_at)
VALUES ('second-admin',$1,'administrator',true,false,$2,$2) RETURNING id::text`, testPasswordVerifier, f.base).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	var otherSession [32]byte
	otherSession[0] = 94
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: otherID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: 1, Verifier: otherSession, Now: f.base.Add(8 * time.Second), RequestID: "other-admin-session"}); err != nil {
		t.Fatal(err)
	}
	other := f.input("actor-two-agent", "shared-key")
	other.Proof.UserID, other.Proof.SessionVerifier, other.Proof.CredentialRevision = otherID, otherSession, 1
	other.Proof.VerifiedAt = f.base.Add(9 * time.Second)
	other.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcded"
	other.Now = func() time.Time { return f.base.Add(10 * time.Second) }
	otherCreated, err := CreateAgent(t.Context(), f.databaseURL, other)
	if err != nil || otherCreated.Replayed || otherCreated.Agent.ID == created.Agent.ID {
		t.Fatalf("other actor creation = %+v, %v", otherCreated, err)
	}
}

func TestIntegrationCreateAgentReceiptExpiryBoundaryAndClockNormalization(t *testing.T) {
	f := newAgentFixture(t)
	input := f.input("clock-agent", "clock-key")
	zone := time.FixedZone("offset", 3600)
	input.Proof.VerifiedAt = f.base.Add(2*time.Second + 123456789*time.Nanosecond).In(zone)
	var clockCalls int
	completion := f.base.Add(3*time.Second + 987654321*time.Nanosecond).In(zone)
	input.Now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return f.base.Add(3*time.Second + 123456789*time.Nanosecond).In(zone)
		}
		return completion
	}
	created, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if clockCalls != 2 {
		t.Fatalf("clock sampled %d times; want twice", clockCalls)
	}
	if created.Agent.CreatedAt.Location() != time.UTC || created.Agent.CreatedAt.Nanosecond()%1000 != 0 || created.Credential.IssuedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("creation timestamps were not normalized: %+v", created)
	}
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	var persistedAgentCreatedAt, persistedAgentUpdatedAt, persistedCredentialIssuedAt time.Time
	if err := conn.QueryRow(t.Context(), `SELECT a.created_at, a.updated_at, c.issued_at
FROM public.almanac_agents a JOIN public.almanac_agent_credentials c ON c.agent_id=a.id WHERE a.id=$1`, created.Agent.ID).Scan(&persistedAgentCreatedAt, &persistedAgentUpdatedAt, &persistedCredentialIssuedAt); err != nil {
		t.Fatal(err)
	}
	for label, value := range map[string]time.Time{"agent.created_at": persistedAgentCreatedAt, "agent.updated_at": persistedAgentUpdatedAt, "credential.issued_at": persistedCredentialIssuedAt} {
		// timestamptz preserves the instant, not the client's display location.
		want := f.base.Add(3*time.Second + 123456*time.Microsecond)
		if !value.Equal(want) || value.Nanosecond()%1000 != 0 {
			t.Fatalf("persisted %s = %v, want instant %v at microsecond precision", label, value, want)
		}
	}
	var expiry, originalReceiptExpiry, receiptCreatedAt time.Time
	if err := conn.QueryRow(t.Context(), "SELECT created_at, expires_at FROM public.almanac_management_receipts WHERE actor_id=$1 AND idempotency_key=$2", f.userID, input.IdempotencyKey).Scan(&receiptCreatedAt, &originalReceiptExpiry); err != nil {
		t.Fatal(err)
	}
	if !receiptCreatedAt.Equal(completion.UTC().Truncate(time.Microsecond)) || receiptCreatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("persisted receipt creation time = %v; want normalized completion time", receiptCreatedAt)
	}
	if !originalReceiptExpiry.Equal(completion.UTC().Truncate(time.Microsecond).Add(24 * time.Hour)) {
		t.Fatalf("receipt expiry = %v", originalReceiptExpiry)
	}

	// A fresh session supports a replay near expiry without extending the stored horizon.
	near := originalReceiptExpiry.Add(-time.Second)
	var nearSession [32]byte
	nearSession[0] = 95
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision, Verifier: nearSession, Now: near.Add(-2 * time.Second), RequestID: "near-expiry-session"}); err != nil {
		t.Fatal(err)
	}
	nearInput := input
	nearInput.Proof.SessionVerifier, nearInput.Proof.VerifiedAt = nearSession, near.Add(-time.Second)
	nearInput.Now = func() time.Time { return near }
	if result, err := CreateAgent(t.Context(), f.databaseURL, nearInput); err != nil || !result.Replayed {
		t.Fatalf("near-expiry replay = %+v, %v", result, err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT expires_at FROM public.almanac_management_receipts WHERE actor_id=$1 AND idempotency_key=$2", f.userID, input.IdempotencyKey).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	if !expiry.Equal(originalReceiptExpiry) {
		t.Fatalf("replay extended receipt expiry from %v to %v", originalReceiptExpiry, expiry)
	}

	boundary := originalReceiptExpiry
	var boundarySession [32]byte
	boundarySession[0] = 96
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision, Verifier: boundarySession, Now: boundary.Add(-2 * time.Second), RequestID: "boundary-session"}); err != nil {
		t.Fatal(err)
	}
	boundaryInput := input
	boundaryInput.Name, boundaryInput.DisplayName = "clock-agent-after-expiry", "Clock agent after expiry"
	boundaryInput.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdec"
	boundaryInput.Proof.SessionVerifier, boundaryInput.Proof.VerifiedAt = boundarySession, boundary.Add(-time.Second)
	boundaryInput.Now = func() time.Time { return boundary }
	boundaryCreated, err := CreateAgent(t.Context(), f.databaseURL, boundaryInput)
	if err != nil || boundaryCreated.Replayed || boundaryCreated.Agent.Name != boundaryInput.Name {
		t.Fatalf("exact-expiry fresh creation = %+v, %v", boundaryCreated, err)
	}
	var remaining int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE actor_id=$1 AND idempotency_key=$2 AND created_at=$3 AND expires_at=$4", f.userID, input.IdempotencyKey, boundary, boundary.Add(24*time.Hour)).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("expired receipt was not replaced at exact boundary: matching rows=%d", remaining)
	}
	boundaryReplay := boundaryInput
	boundaryReplay.CredentialID = "abcdefab-cdef-4abc-8def-abcdefabcdea"
	boundaryReplay.CredentialVerifier = [32]byte{98}
	if replay, err := CreateAgent(t.Context(), f.databaseURL, boundaryReplay); err != nil || !replay.Replayed || replay.Agent.ID != boundaryCreated.Agent.ID {
		t.Fatalf("replay after exact-boundary fresh write = %+v, %v", replay, err)
	}
}

func TestIntegrationCreateAgentProofAndSessionExpiryAfterLockWait(t *testing.T) {
	for _, test := range []struct {
		name    string
		start   time.Duration
		proof   time.Duration
		advance time.Duration
	}{
		{"proof", 3 * time.Second, 2 * time.Second, 35 * time.Second},
		// Session issued at base+1s: cross its exact 30-minute idle boundary
		// while password proof remains only three seconds old.
		{"session", 30*time.Minute - time.Second, 30*time.Minute - 2*time.Second, 30*time.Minute + time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAgentFixture(t)
			blocker := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(blocker)
			tx, err := blocker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			if _, err := tx.Exec(t.Context(), "SELECT id FROM public.almanac_users WHERE id=$1 FOR UPDATE", f.userID); err != nil {
				t.Fatal(err)
			}
			var clockNanos atomic.Int64
			clockNanos.Store(f.base.Add(test.start).UnixNano())
			input := f.input("lock-clock-agent", "lock-clock-key")
			input.Proof.VerifiedAt = f.base.Add(test.proof)
			input.Now = func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() }
			result := make(chan error, 1)
			go func() { _, err := CreateAgent(t.Context(), f.databaseURL, input); result <- err }()
			observer := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(observer)
			waitForAgentLockWait(t, observer)
			clockNanos.Store(f.base.Add(test.advance).UnixNano())
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("post-lock authority result = %v", err)
			}
			if counts := agentRowCounts(t, f.databaseURL); counts != (agentCounts{}) {
				t.Fatalf("lock-wait denial left rows: %+v", counts)
			}
			closeIntegrationConn(observer)
			closeIntegrationConn(blocker)
		})
	}
}

func TestIntegrationCreateAgentOrderedAfterLogoutOrPasswordChange(t *testing.T) {
	for _, change := range []string{"logout", "password-change"} {
		t.Run(change, func(t *testing.T) {
			f := newAgentFixture(t)
			const advisoryKey int64 = 1770141004
			blocker := integrationConnect(t, f.databaseURL)
			if _, err := blocker.Exec(t.Context(), "SELECT pg_advisory_lock($1)", advisoryKey); err != nil {
				t.Fatal(err)
			}
			conn := integrationConnect(t, f.databaseURL)
			if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.pause_agent_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(1770141004); RETURN OLD; END $$;
CREATE TRIGGER pause_agent_invalidation BEFORE DELETE ON public.almanac_sessions FOR EACH ROW EXECUTE FUNCTION public.pause_agent_invalidation()`); err != nil {
				t.Fatal(err)
			}
			invalidation := make(chan error, 1)
			if change == "logout" {
				go func() {
					_, err := RevokeSession(t.Context(), f.databaseURL, f.verifier, f.base.Add(3*time.Second), "ordered-logout")
					invalidation <- err
				}()
			} else {
				var nextSession [32]byte
				nextSession[0] = 97
				go func() {
					_, err := RotateLocalPassword(t.Context(), f.databaseURL, RotatePasswordInput{
						CurrentSessionVerifier: f.verifier, ExpectedCredentialRevision: f.revision, ExpectedCredentialEpoch: f.epoch,
						NewPasswordVerifier: testPasswordVerifier,
						NewSessionVerifier:  nextSession, Now: f.base.Add(3 * time.Second), RequestID: "ordered-password-change",
					})
					invalidation <- err
				}()
			}
			defer closeIntegrationConn(blocker)
			observer := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(observer)
			waitForAgentQueryWait(t, observer, "%DELETE FROM public.almanac_sessions%")
			input := f.input("invalidated-agent", "invalidated-key")
			result := make(chan error, 1)
			go func() { _, err := CreateAgent(t.Context(), f.databaseURL, input); result <- err }()
			if change == "password-change" {
				waitForAgentQueryWait(t, observer, "%FROM public.almanac_installation%")
			} else {
				waitForAgentLockWait(t, observer)
			}
			if _, err := blocker.Exec(t.Context(), "SELECT pg_advisory_unlock($1)", advisoryKey); err != nil {
				t.Fatal(err)
			}
			if err := <-invalidation; err != nil {
				t.Fatalf("%s failed: %v", change, err)
			}
			if err := <-result; !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("post-%s CreateAgent = %v", change, err)
			}
			if counts := agentRowCounts(t, f.databaseURL); counts != (agentCounts{}) {
				t.Fatalf("post-%s denial left rows: %+v", change, counts)
			}
			if _, err := conn.Exec(t.Context(), "DROP TRIGGER pause_agent_invalidation ON public.almanac_sessions; DROP FUNCTION public.pause_agent_invalidation()"); err != nil {
				t.Fatal(err)
			}
			closeIntegrationConn(conn)
			closeIntegrationConn(observer)
			closeIntegrationConn(blocker)
		})
	}
}

func TestIntegrationCreateAgentCancellationDuringUserLockLeavesNoRows(t *testing.T) {
	f := newAgentFixture(t)
	blocker := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(blocker)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "SELECT id FROM public.almanac_users WHERE id=$1 FOR UPDATE", f.userID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	input := f.input("cancel-agent", "cancel-key")
	result := make(chan error, 1)
	go func() { _, err := CreateAgent(ctx, f.databaseURL, input); result <- err }()
	observer := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentLockWait(t, observer)
	cancel()
	if err := <-result; !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancelled registration error = %v", err)
	}
	if counts := agentRowCounts(t, f.databaseURL); counts != (agentCounts{}) {
		t.Fatalf("cancelled request left rows: %+v", counts)
	}
	_ = tx.Rollback(t.Context())
	closeIntegrationConn(observer)
	closeIntegrationConn(blocker)
}

func TestIntegrationCreateAgentAuditAndReceiptFailuresRollBack(t *testing.T) {
	for _, point := range []string{"audit", "receipt"} {
		t.Run(point, func(t *testing.T) {
			f := newAgentFixture(t)
			conn := integrationConnect(t, f.databaseURL)
			if point == "audit" {
				_, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_agent_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='agent.created' THEN RAISE EXCEPTION 'test audit failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_agent_audit BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW EXECUTE FUNCTION public.fail_agent_audit()`)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_agent_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test receipt failure'; END $$;
CREATE TRIGGER fail_agent_receipt BEFORE INSERT ON public.almanac_management_receipts FOR EACH ROW EXECUTE FUNCTION public.fail_agent_receipt()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			input := f.input("rollback-agent", "rollback-key")
			if _, err := CreateAgent(t.Context(), f.databaseURL, input); err == nil || strings.Contains(err.Error(), input.Name) || strings.Contains(err.Error(), string(input.CredentialVerifier[:])) {
				t.Fatalf("injected %s failure = %v", point, err)
			}
			if counts := agentRowCounts(t, f.databaseURL); counts != (agentCounts{}) {
				t.Fatalf("%s failure left rows: %+v", point, counts)
			}
			closeIntegrationConn(conn)
		})
	}
}

type agentCounts struct{ agents, credentials, receipts, audits int }

func agentRowCounts(t *testing.T, databaseURL string) agentCounts {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var counts agentCounts
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agents").Scan(&counts.agents); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials").Scan(&counts.credentials); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts").Scan(&counts.receipts); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action IN ('agent.created','agent.credential.issued')").Scan(&counts.audits); err != nil {
		t.Fatal(err)
	}
	return counts
}

func updateAgentUser(t *testing.T, f *agentFixture, assignment string) {
	t.Helper()
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	if _, err := conn.Exec(t.Context(), fmt.Sprintf("UPDATE public.almanac_users SET %s WHERE id=$1", assignment), f.userID); err != nil {
		t.Fatal(err)
	}
}

func waitForAgentLockWait(t *testing.T, observer *pgx.Conn) {
	t.Helper()
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_users%")
}

func waitForAgentQueryWait(t *testing.T, observer *pgx.Conn, pattern string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND wait_event_type='Lock' AND query ILIKE $1`, pattern).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-t.Context().Done():
			t.Fatalf("context ended observing agent lock: %v", t.Context().Err())
		}
	}
	t.Fatal("CreateAgent did not reach the user lock")
}
