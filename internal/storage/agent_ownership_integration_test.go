//go:build integration

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationFreshAgentReplacesUnexpiredOwner(t *testing.T) {
	f := newAgentFixture(t)
	issued, _ := createOwnershipAgent(t, f, "ownership-replacement")
	now := f.base.Add(4 * time.Second)
	clock := func() time.Time { return now }
	oldProof := ownershipVerifier(ownershipBytes(32, 81))
	newProof := ownershipVerifier(ownershipBytes(32, 82))
	old, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, oldProof, clock)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, newProof, clock)
	if err != nil || replacement.Generation != old.Generation+1 || !now.Before(old.ExpiresAt) {
		t.Fatalf("fresh replacement = %+v, %v", replacement, err)
	}
	if _, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, oldProof, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("superseded proof replay = %v", err)
	}
	if _, err := ResumeAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, oldProof, old.Generation, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("superseded resume = %v", err)
	}
	if _, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, oldProof, old.Generation, 0, ownershipNonce(83), clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("superseded renewal = %v", err)
	}
	replay, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, newProof, clock)
	if err != nil || replay.Generation != replacement.Generation || !replay.ExpiresAt.Equal(replacement.ExpiresAt) {
		t.Fatalf("replacement welcome replay = %+v, %v", replay, err)
	}
	now = replacement.ExpiresAt
	if _, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, oldProof, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("superseded proof after expiry = %v", err)
	}
}

func TestIntegrationAgentOwnershipAcquireResumeRenewAndReplay(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "ownership-lifecycle")
	secret := ownershipBytes(32, 3)
	now := f.base.Add(4 * time.Second)
	clock := func() time.Time { return now }
	acquire := func(proof []byte) (AgentOwnershipResult, error) {
		return AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), clock)
	}
	first, err := acquire(secret)
	if err != nil || first.Generation != 1 || first.RenewalSequence != 0 || first.AgentID != created.Agent.ID {
		t.Fatalf("first acquisition = %+v, %v", first, err)
	}
	backwards := now.Add(-time.Second)
	if _, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), func() time.Time { return backwards }); !errors.Is(err, ErrAgentOwnershipInput) {
		t.Fatalf("backward acquisition replay clock error = %v", err)
	}
	if _, err := ResumeAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), first.Generation, func() time.Time { return backwards }); !errors.Is(err, ErrAgentOwnershipInput) {
		t.Fatalf("backward resume clock error = %v", err)
	}
	// Each operation opens a new connection. This proves the persisted state is the authority.
	replayed, err := acquire(secret)
	if err != nil || replayed.Generation != first.Generation || !replayed.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("lost response replay = %+v, %v", replayed, err)
	}
	resumed, err := ResumeAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), first.Generation, clock)
	if err != nil || resumed.Generation != first.Generation || !resumed.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("resume = %+v, %v", resumed, err)
	}
	if _, err := ResumeAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(ownershipBytes(32, 5)), first.Generation, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("wrong resume proof error = %v", err)
	}
	nonce := ownershipNonce(6)
	now = now.Add(10 * time.Second)
	renewed, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), first.Generation, 0, nonce, clock)
	if err != nil || renewed.RenewalSequence != 1 || !bytes.Equal(renewed.Nonce, nonce[:]) {
		t.Fatalf("renewal = %+v, %v", renewed, err)
	}
	// A same-sequence nonce replay returns the original authority bounds.
	now = now.Add(time.Minute)
	replay, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), first.Generation, 0, nonce, clock)
	if err != nil || replay.RenewalSequence != renewed.RenewalSequence || !replay.ExpiresAt.Equal(renewed.ExpiresAt) || !replay.IssuedAt.Equal(renewed.IssuedAt) {
		t.Fatalf("renewal replay = %+v, %v", replay, err)
	}
	if _, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), first.Generation, 0, ownershipNonce(7), clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("stale renewal sequence error = %v", err)
	}
	backwards = renewed.IssuedAt.Add(-time.Second)
	if _, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), first.Generation, 1, ownershipNonce(8), func() time.Time { return backwards }); !errors.Is(err, ErrAgentOwnershipInput) {
		t.Fatalf("backward renewal clock error = %v", err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var generation, sequence int64
	if err := conn.QueryRow(t.Context(), "SELECT generation, renewal_sequence FROM public.almanac_agent_ownership WHERE agent_id=$1", created.Agent.ID).Scan(&generation, &sequence); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if generation != 1 || sequence != 1 {
		t.Fatalf("failed renewal changed counters: generation=%d sequence=%d", generation, sequence)
	}
}

func TestIntegrationAgentOwnershipExpiryRotationRevocationAndFencing(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "ownership-expiry")
	secret := ownershipBytes(32, 9)
	now := f.base.Add(4 * time.Second)
	clock := func() time.Time { return now }
	first, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(secret), clock)
	if err != nil {
		t.Fatal(err)
	}
	newCredential, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	proof := f.input("unused", "unused").Proof
	if _, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{
		Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, Overlap: new(time.Duration),
		CredentialID: newCredential.ID, CredentialVerifier: newCredential.Verifier,
		IdempotencyKey: "ownership-rotate", RequestID: "ownership-rotate", Now: func() time.Time { return now },
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeAgentOwnership(t.Context(), f.databaseURL, newCredential.ID, newCredential.Verifier,
		ownershipVerifier(secret), first.Generation, clock)
	if err != nil || resumed.Generation != first.Generation {
		t.Fatalf("rotated credential did not resume owner: %+v %v", resumed, err)
	}
	// Exact expiry denies renewal/resume and permits replacement by a fresh process.
	now = first.ExpiresAt
	if _, err := ResumeAgentOwnership(t.Context(), f.databaseURL, newCredential.ID, newCredential.Verifier,
		ownershipVerifier(secret), first.Generation, clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("exact expiry resume error = %v", err)
	}
	if _, err := RenewAgentOwnership(t.Context(), f.databaseURL, newCredential.ID, newCredential.Verifier,
		ownershipVerifier(secret), first.Generation, 0, ownershipNonce(12), clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("exact expiry renewal error = %v", err)
	}
	if _, err := AcquireAgentOwnership(t.Context(), f.databaseURL, newCredential.ID, newCredential.Verifier,
		ownershipVerifier(secret), clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("expired acquisition replay error = %v", err)
	}
	newSecret := ownershipBytes(32, 10)
	second, err := AcquireAgentOwnership(t.Context(), f.databaseURL, newCredential.ID, newCredential.Verifier,
		ownershipVerifier(newSecret), clock)
	if err != nil || second.Generation != first.Generation+1 {
		t.Fatalf("expiry replacement = %+v %v", second, err)
	}
	if _, err := RenewAgentOwnership(t.Context(), f.databaseURL, newCredential.ID, newCredential.Verifier,
		ownershipVerifier(secret), first.Generation, 1, ownershipNonce(11), clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("fenced generation renewal error = %v", err)
	}
	var freshSession [32]byte
	freshSession[0] = 100
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{
		UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision,
		Verifier: freshSession, Now: now, RequestID: "ownership-revoke-session",
	}); err != nil {
		t.Fatal(err)
	}
	proof.SessionVerifier, proof.VerifiedAt = freshSession, now
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{
		Proof: proof, AgentID: created.Agent.ID, CredentialID: newCredential.ID, ExpectedVersion: 2,
		IdempotencyKey: "ownership-revoke", RequestID: "ownership-revoke", Now: func() time.Time { return now.Add(time.Second) },
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeAgentOwnership(t.Context(), f.databaseURL, newCredential.ID, newCredential.Verifier,
		ownershipVerifier(newSecret), second.Generation, func() time.Time { return now.Add(time.Second) }); !errors.Is(err, ErrInvalidAgentCredential) {
		t.Fatalf("revoked credential resume error = %v", err)
	}
}

func TestIntegrationAgentOwnershipConcurrentAcquisitionFencesEarlierGeneration(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "ownership-concurrent")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	type outcome struct {
		result AgentOwnershipResult
		proof  []byte
		err    error
	}
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for i := range 2 {
		proof := ownershipBytes(32, byte(20+i))
		workers.Go(func() {
			<-start
			result, err := AcquireAgentOwnership(ctx, f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), func() time.Time { return f.base.Add(4 * time.Second) })
			results <- outcome{result, proof, err}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var generations = map[int64][]byte{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("unexpected acquisition error: %v", result.err)
		}
		generations[result.result.Generation] = result.proof
	}
	if len(generations) != 2 || generations[1] == nil || generations[2] == nil {
		t.Fatalf("concurrent generations = %v", generations)
	}
	if _, err := AcquireAgentOwnership(ctx, f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(generations[1]), func() time.Time { return f.base.Add(4 * time.Second) }); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("earlier proof replay = %v", err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var count int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1", created.Agent.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if count != 2 {
		t.Fatalf("concurrent acquisition stored %d owners", count)
	}
}

func TestIntegrationAgentOwnershipNullableRenewalMetadataConstraint(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "ownership-constraint")
	_, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(ownershipBytes(32, 30)), func() time.Time { return f.base.Add(4 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, f.databaseURL)
	_, err = conn.Exec(t.Context(), "UPDATE public.almanac_agent_ownership SET renewal_sequence=1, last_renewal_issued_at=renewed_at, last_renewal_expires_at=expires_at WHERE agent_id=$1", created.Agent.ID)
	closeIntegrationConn(conn)
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgErr.Code != "23514" {
		t.Fatalf("NULL nonce constraint error = %v, want check violation 23514", err)
	}
}

func TestIntegrationAgentOwnershipClampsToCredentialExpiryAndRetirement(t *testing.T) {
	t.Run("credential expiry", func(t *testing.T) {
		f := newAgentFixture(t)
		issued, err := identity.NewAgentToken()
		if err != nil {
			t.Fatal(err)
		}
		input := f.input("ownership-short-credential", "ownership-short-credential-create")
		input.CredentialID, input.CredentialVerifier, input.Lifetime = issued.ID, issued.Verifier, 5*time.Minute
		created, err := CreateAgent(t.Context(), f.databaseURL, input)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
			ownershipVerifier(ownershipBytes(32, 42)), func() time.Time { return f.base.Add(4 * time.Second) })
		if err != nil || !owner.ExpiresAt.Equal(created.Credential.ExpiresAt) {
			t.Fatalf("credential-expiry-clamped owner=%+v credential=%+v err=%v", owner, created.Credential, err)
		}
	})

	t.Run("credential retirement", func(t *testing.T) {
		f := newAgentFixture(t)
		issued, created := createOwnershipAgent(t, f, "ownership-retirement-clamp")
		replacement, err := identity.NewAgentToken()
		if err != nil {
			t.Fatal(err)
		}
		now := f.base.Add(4 * time.Second)
		overlap := 10 * time.Minute
		rotated, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{
			Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1,
			Overlap: &overlap, CredentialID: replacement.ID, CredentialVerifier: replacement.Verifier,
			IdempotencyKey: "ownership-retirement-rotate", RequestID: "ownership-retirement-rotate",
			Now: func() time.Time { return now },
		})
		if err != nil || rotated.PreviousCredential == nil || rotated.PreviousCredential.RetireAt == nil {
			t.Fatalf("credential rotation=%+v err=%v", rotated, err)
		}
		owner, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
			ownershipVerifier(ownershipBytes(32, 43)), func() time.Time { return now.Add(time.Second) })
		if err != nil || !owner.ExpiresAt.Equal(*rotated.PreviousCredential.RetireAt) {
			t.Fatalf("retirement-clamped owner=%+v retirement=%v err=%v", owner, rotated.PreviousCredential.RetireAt, err)
		}
	})
}

func TestIntegrationAgentOwnershipRenewalCannotShortenExistingGrantAfterRotation(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "ownership-short-renewal")
	secret := ownershipBytes(32, 44)
	now := f.base.Add(4 * time.Second)
	owner, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	shortLifetime, overlap := 5*time.Minute, 10*time.Minute
	if _, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{
		Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1,
		Lifetime: shortLifetime, Overlap: &overlap, CredentialID: replacement.ID,
		CredentialVerifier: replacement.Verifier, IdempotencyKey: "ownership-short-rotate",
		RequestID: "ownership-short-rotate", Now: func() time.Time { return now.Add(time.Second) },
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := RenewAgentOwnership(t.Context(), f.databaseURL, replacement.ID, replacement.Verifier,
		ownershipVerifier(secret), owner.Generation, 0, ownershipNonce(45), func() time.Time { return now.Add(2 * time.Second) }); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("shortened-authority renewal error=%v", err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var expiresAt time.Time
	var sequence int64
	if err := conn.QueryRow(t.Context(), "SELECT expires_at, renewal_sequence FROM public.almanac_agent_ownership WHERE agent_id=$1", created.Agent.ID).Scan(&expiresAt, &sequence); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if !expiresAt.Equal(owner.ExpiresAt) || sequence != 0 {
		t.Fatalf("denied renewal changed existing authority: expiry=%s sequence=%d", expiresAt, sequence)
	}
}

func TestIntegrationAgentOwnershipCounterExhaustionFailsClosed(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "ownership-exhaustion")
	secret := ownershipBytes(32, 31)
	now := f.base.Add(4 * time.Second)
	owner, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	maximum := int64(^uint64(0) >> 1)
	conn := integrationConnect(t, f.databaseURL)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal("begin counter exhaustion fixture failed")
	}
	if _, err := tx.Exec(t.Context(), "DELETE FROM public.almanac_agent_execution_grants WHERE agent_id=$1", created.Agent.ID); err != nil {
		t.Fatal("remove initial grant for counter fixture failed")
	}
	nonce := ownershipBytes(32, 32)
	if _, err := tx.Exec(t.Context(), "UPDATE public.almanac_agent_ownership SET generation=$2, renewal_sequence=$2, last_nonce=$3, last_renewal_issued_at=renewed_at, last_renewal_expires_at=expires_at WHERE agent_id=$1", created.Agent.ID, maximum, nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_agent_execution_grants
(lease_id,agent_id,generation,configuration_revision,allowed_measurements,valid_from,valid_until,renewal_sequence,request_nonce,retain_until)
VALUES($1,$2,$3,$4,'[]'::jsonb,$5,$6,$3,$7,$6::timestamptz+interval '7 days')`, owner.LeaseID, created.Agent.ID, maximum,
		owner.ConfigurationRevision, owner.LeaseValidFrom, owner.LeaseValidUntil, nonce); err != nil {
		t.Fatal("insert coherent max-counter execution grant failed")
	}
	if _, err := tx.Exec(t.Context(), "UPDATE public.almanac_agent_ownership SET current_lease_id=$2 WHERE agent_id=$1", created.Agent.ID, owner.LeaseID); err != nil {
		t.Fatal("point max-counter owner at execution grant failed")
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal("commit counter exhaustion fixture failed")
	}
	closeIntegrationConn(conn)
	if _, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(secret), maximum, maximum, [32]byte{33}, func() time.Time { return now.Add(time.Second) }); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("max renewal sequence error = %v", err)
	}
	if _, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(ownershipBytes(32, 34)), func() time.Time { return owner.ExpiresAt }); !errors.Is(err, ErrAgentOwnershipExhausted) {
		t.Fatalf("max ownership generation error = %v", err)
	}
	conn = integrationConnect(t, f.databaseURL)
	var generation, sequence int64
	if err := conn.QueryRow(t.Context(), "SELECT generation, renewal_sequence FROM public.almanac_agent_ownership WHERE agent_id=$1", created.Agent.ID).Scan(&generation, &sequence); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if generation != maximum || sequence != maximum {
		t.Fatalf("exhausted counters wrapped or changed: generation=%d sequence=%d", generation, sequence)
	}
}

func TestIntegrationAgentOwnershipRejectsDisabledAndStaleEpochCredentials(t *testing.T) {
	for _, scenario := range []string{"disabled", "epoch"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAgentFixture(t)
			issued, created := createOwnershipAgent(t, f, "ownership-"+scenario)
			if scenario == "disabled" {
				if _, err := DisableAgent(t.Context(), f.databaseURL, DisableAgentInput{
					Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID,
					ExpectedVersion: 1, IdempotencyKey: "ownership-disable", RequestID: "ownership-disable",
					Now: func() time.Time { return f.base.Add(4 * time.Second) },
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				conn := integrationConnect(t, f.databaseURL)
				_, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET credential_epoch='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb' WHERE singleton")
				closeIntegrationConn(conn)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
				ownershipVerifier(ownershipBytes(32, 40)), func() time.Time { return f.base.Add(5 * time.Second) }); !errors.Is(err, ErrInvalidAgentCredential) {
				t.Fatalf("ownership acquisition with %s agent credential error = %v", scenario, err)
			}
			conn := integrationConnect(t, f.databaseURL)
			var count int
			if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1", created.Agent.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			closeIntegrationConn(conn)
			if count != 0 {
				t.Fatalf("denied %s acquisition left %d ownership rows", scenario, count)
			}
		})
	}
}

func TestIntegrationAgentOwnershipOrdersBehindCredentialRevocation(t *testing.T) {
	f := newAgentFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	issued, created := createOwnershipAgent(t, f, "ownership-revoke-order")
	proof := f.input("unused", "unused").Proof
	lockConn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(lockConn)
	lockTx, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var locked string
	if err := lockTx.QueryRow(ctx, "SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE", created.Agent.ID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(lockTx)
	workers := make(chan struct{}, 2)
	defer func() {
		cancel()
		rollbackTransaction(lockTx)
		for range 2 {
			select {
			case <-workers:
			case <-time.After(3 * time.Second):
				t.Error("ownership/revocation worker did not stop")
				return
			}
		}
	}()
	revoked := make(chan error, 1)
	go func() {
		defer func() { workers <- struct{}{} }()
		_, err := RevokeAgentCredential(ctx, f.databaseURL, RevokeAgentCredentialInput{
			Proof: proof, AgentID: created.Agent.ID, CredentialID: issued.ID, ExpectedVersion: 1,
			IdempotencyKey: "ownership-order-revoke", RequestID: "ownership-order-revoke",
			Now: func() time.Time { return f.base.Add(4 * time.Second) },
		})
		revoked <- err
	}()
	if err := waitForAgentAccessLockWait(ctx, t, f, "SELECT id, name, display_name, enabled, version"); err != nil {
		t.Fatal("revocation did not queue on the locked agent")
	}
	acquired := make(chan error, 1)
	go func() {
		defer func() { workers <- struct{}{} }()
		_, err := AcquireAgentOwnership(ctx, f.databaseURL, issued.ID, issued.Verifier,
			ownershipVerifier(ownershipBytes(32, 41)), func() time.Time { return f.base.Add(5 * time.Second) })
		acquired <- err
	}()
	if err := waitForAgentAccessLockWait(ctx, t, f, "SELECT id, enabled FROM public.almanac_agents"); err != nil {
		t.Fatal("ownership operation did not queue behind revocation")
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if err := <-acquired; !errors.Is(err, ErrInvalidAgentCredential) {
		t.Fatalf("ownership operation after revocation = %v", err)
	}
}

func createOwnershipAgent(t *testing.T, f agentFixture, name string) (identity.AgentToken, AgentCreation) {
	t.Helper()
	issued, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	input := f.input(name, name+"-create")
	input.CredentialID, input.CredentialVerifier = issued.ID, issued.Verifier
	created, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	return issued, created
}

func ownershipBytes(size int, first byte) []byte {
	value := make([]byte, size)
	if size > 0 {
		value[0] = first
	}
	return value
}

func ownershipVerifier(value []byte) [32]byte {
	return sha256.Sum256(value)
}

func ownershipNonce(first byte) [32]byte {
	var nonce [32]byte
	nonce[0] = first
	return nonce
}
