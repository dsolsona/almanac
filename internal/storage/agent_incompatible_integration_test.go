//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
)

func TestIntegrationIncompatibleAttemptIsVisibleWithoutOwnershipAndFenced(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "incompatible-runtime")
	proof := ownershipBytes(32, 81)
	now := f.base.Add(4 * time.Second)
	clock := func() time.Time { return now }
	revision, err := RecordAgentIncompatibleAttempt(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), 0, clock)
	if err != nil || revision != 1 {
		t.Fatalf("record initial incompatible attempt revision=%d err=%v", revision, err)
	}
	var owners, grants, configurations, attempts int
	conn := integrationConnect(t, f.databaseURL)
	if err := conn.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_configurations WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_incompatible_attempts WHERE agent_id=$1)`, created.Agent.ID).Scan(&owners, &grants, &configurations, &attempts); err != nil {
		t.Fatal("read initial incompatible state failed")
	}
	closeIntegrationConn(conn)
	if owners != 0 || grants != 0 || configurations != 1 || attempts != 1 {
		t.Fatalf("initial incompatible owner/grant/config/attempt counts=%d/%d/%d/%d", owners, grants, configurations, attempts)
	}
	read := func(at time.Time) AgentRecord {
		t.Helper()
		record, err := GetAgent(t.Context(), f.databaseURL, AgentReadInput{
			SessionVerifier: f.verifier, AgentID: created.Agent.ID, Now: func() time.Time { return at },
		})
		if err != nil {
			t.Fatalf("read agent at %s: %v", at, err)
		}
		return record
	}
	initial := read(now)
	if initial.Runtime == nil || initial.Runtime.Reachability != "incompatible" || initial.Runtime.Protocol != "incompatible" ||
		initial.Runtime.SoftwareVersion != "unknown" || initial.Runtime.Capabilities == nil || len(initial.Runtime.Capabilities) != 0 ||
		initial.Runtime.DesiredConfigurationRevision != 1 || initial.Runtime.AppliedConfigurationRevision != 0 ||
		initial.Runtime.SchedulerHealth != "degraded" || initial.Runtime.CollectionProgress != "not_started" ||
		initial.Runtime.LastHeartbeatAt != nil || initial.Runtime.LastAttemptAt != nil || initial.Runtime.LastSuccessfulQueryAt != nil || initial.Runtime.NextExpectedExecutionAt != nil {
		t.Fatalf("synthetic incompatible status fabricated or omitted fields: %+v", initial.Runtime)
	}

	hello := &AgentRuntimeHello{Protocol: "almanac.agent.v1", SoftwareVersion: "1.0.0", Capabilities: []byte(`[]`)}
	now = now.Add(time.Second)
	owner, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), hello, clock)
	if err != nil || owner.Generation != 1 {
		t.Fatalf("compatible ownership acquisition = %+v, %v", owner, err)
	}
	connected := read(now)
	if connected.Runtime == nil || connected.Runtime.Reachability != "connected" {
		t.Fatalf("compatible hello did not clear incompatible state: %+v", connected.Runtime)
	}
	if err := RecordAgentConfigurationApplied(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), owner.Generation, 1, clock); err != nil {
		t.Fatalf("record applied config for runtime preservation fixture: %v", err)
	}
	now = now.Add(10 * time.Second)
	lastAttempt, lastSuccess, nextExpected := now.Add(-2*time.Second), now.Add(-time.Second), now.Add(time.Minute)
	heartbeat := &AgentRuntimeHeartbeat{
		ReceivedAt: now, LeaseID: owner.LeaseID, AppliedConfigurationRevision: 1,
		SchedulerHealth: "ready", CollectionProgress: "idle",
		LastAttemptAt: &lastAttempt, LastSuccessfulQueryAt: &lastSuccess, NextExpectedExecutionAt: &nextExpected,
	}
	owner, err = RenewAgentOwnershipWithHeartbeat(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), owner.Generation, 0, ownershipNonce(83), heartbeat, clock)
	if err != nil || owner.RenewalSequence != 1 {
		t.Fatalf("persist runtime timestamps: owner=%+v err=%v", owner, err)
	}
	connected = read(now)
	if connected.Runtime == nil || connected.Runtime.LastHeartbeatAt == nil || connected.Runtime.LastAttemptAt == nil ||
		connected.Runtime.LastSuccessfulQueryAt == nil || connected.Runtime.NextExpectedExecutionAt == nil {
		t.Fatalf("heartbeat timestamp preservation fixture incomplete: %+v", connected.Runtime)
	}

	// Generation zero with the matching proof is allowed for a lost-welcome
	// replay; it represents the same owner, not a copied-token takeover.
	now = now.Add(time.Second)
	if _, err := RecordAgentIncompatibleAttempt(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), 0, clock); err != nil {
		t.Fatalf("same-proof initial-generation retry: %v", err)
	}
	sameProofIncompatible := read(now)
	if sameProofIncompatible.Runtime == nil || sameProofIncompatible.Runtime.Reachability != "incompatible" {
		t.Fatalf("same-proof retry was not observable: %+v", sameProofIncompatible.Runtime)
	}
	now = now.Add(time.Second)
	resumedInitial, err := AcquireAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), hello, clock)
	if err != nil || resumedInitial.Generation != owner.Generation || resumedInitial.LeaseID != owner.LeaseID {
		t.Fatalf("same-proof lost-welcome resume changed ownership: %+v err=%v", resumedInitial, err)
	}
	connected = read(now)
	if connected.Runtime == nil || connected.Runtime.Reachability != "connected" {
		t.Fatalf("same-proof compatible resume did not clear incompatible state: %+v", connected.Runtime)
	}

	wrongProof := ownershipVerifier(ownershipBytes(32, 82))
	now = now.Add(time.Second)
	if _, err := RecordAgentIncompatibleAttempt(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		wrongProof, 0, clock); !errors.Is(err, ErrAgentOwnershipConflict) {
		t.Fatalf("copied-token incompatible attempt error=%v, want active-owner conflict", err)
	}
	stillConnected := read(now)
	if stillConnected.Runtime == nil || stillConnected.Runtime.Reachability != "connected" {
		t.Fatalf("copied-token incompatible attempt changed active status: %+v", stillConnected.Runtime)
	}
	conn = integrationConnect(t, f.databaseURL)
	if err := conn.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_incompatible_attempts WHERE agent_id=$1)`, created.Agent.ID).Scan(&owners, &grants, &attempts); err != nil {
		t.Fatal("read fenced incompatible state failed")
	}
	closeIntegrationConn(conn)
	if owners != 1 || grants != 2 || attempts != 0 {
		t.Fatalf("fenced incompatible owner/grant/attempt counts=%d/%d/%d", owners, grants, attempts)
	}

	now = now.Add(time.Second)
	revision, err = RecordAgentIncompatibleAttempt(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), owner.Generation, clock)
	if err != nil || revision != 1 {
		t.Fatalf("record same-owner incompatible attempt revision=%d err=%v", revision, err)
	}
	incompatible := read(now)
	expectedIncompatible := *connected.Runtime
	expectedIncompatible.Reachability = "incompatible"
	if incompatible.Runtime == nil || !reflect.DeepEqual(incompatible.Runtime, &expectedIncompatible) {
		t.Fatalf("incompatible attempt rewrote observed runtime fields: before=%+v after=%+v", connected.Runtime, incompatible.Runtime)
	}

	now = now.Add(time.Second)
	if _, err := ResumeAgentOwnershipWithHello(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), owner.Generation, hello, clock); err != nil {
		t.Fatalf("compatible same-owner resume: %v", err)
	}
	resumed := read(now)
	if resumed.Runtime == nil || resumed.Runtime.Reachability != "connected" {
		t.Fatalf("compatible resume did not clear incompatible state: %+v", resumed.Runtime)
	}
	now = now.Add(time.Second)
	if _, err := RecordAgentIncompatibleAttempt(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
		ownershipVerifier(proof), owner.Generation+1, clock); !errors.Is(err, ErrAgentOwnershipConflict) {
		t.Fatalf("wrong positive generation incompatible attempt error=%v", err)
	}
	stillConnected = read(now)
	if stillConnected.Runtime == nil || stillConnected.Runtime.Reachability != "connected" ||
		!reflect.DeepEqual(stillConnected.Runtime.LastHeartbeatAt, resumed.Runtime.LastHeartbeatAt) ||
		!reflect.DeepEqual(stillConnected.Runtime.LastSuccessfulQueryAt, resumed.Runtime.LastSuccessfulQueryAt) {
		t.Fatalf("wrong generation changed active runtime status: %+v", stillConnected.Runtime)
	}
}

func TestIntegrationIncompatibleAttemptRejectsExpiredCredentialsAndOwner(t *testing.T) {
	t.Run("expired credential", func(t *testing.T) {
		f := newAgentFixture(t)
		issued, err := identity.NewAgentToken()
		if err != nil {
			t.Fatal(err)
		}
		input := f.input("incompatible-expired-credential", "incompatible-expired-create")
		input.CredentialID, input.CredentialVerifier, input.Lifetime = issued.ID, issued.Verifier, 5*time.Minute
		created, err := CreateAgent(t.Context(), f.databaseURL, input)
		if err != nil {
			t.Fatal(err)
		}
		at := created.Credential.ExpiresAt.Add(time.Second)
		_, err = RecordAgentIncompatibleAttempt(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
			ownershipVerifier(ownershipBytes(32, 91)), 0, func() time.Time { return at })
		assertIncompatibleAttemptDenied(t, f.databaseURL, created.Agent.ID, err, ErrInvalidAgentCredential, 0, 0, 0, 0)
	})

	t.Run("revoked credential", func(t *testing.T) {
		f := newAgentFixture(t)
		issued, created := createOwnershipAgent(t, f, "incompatible-revoked-credential")
		at := f.base.Add(4 * time.Second)
		if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{
			Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID,
			CredentialID: created.Credential.ID, ExpectedVersion: 1,
			IdempotencyKey: "incompatible-revoked-credential", RequestID: "incompatible-revoked-credential",
			Now: func() time.Time { return at },
		}); err != nil {
			t.Fatal("revoke incompatible test credential failed")
		}
		at = at.Add(time.Second)
		_, err := RecordAgentIncompatibleAttempt(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
			ownershipVerifier(ownershipBytes(32, 92)), 0, func() time.Time { return at })
		assertIncompatibleAttemptDenied(t, f.databaseURL, created.Agent.ID, err, ErrInvalidAgentCredential, 0, 0, 0, 0)
	})

	t.Run("expired positive-generation owner", func(t *testing.T) {
		f := newAgentFixture(t)
		issued, created := createOwnershipAgent(t, f, "incompatible-expired-owner")
		proof := ownershipBytes(32, 93)
		owner, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
			ownershipVerifier(proof), func() time.Time { return f.base.Add(4 * time.Second) })
		if err != nil {
			t.Fatal("acquire owner for expiry fixture failed")
		}
		at := owner.ExpiresAt.Add(time.Second)
		_, err = RecordAgentIncompatibleAttempt(t.Context(), f.databaseURL, issued.ID, issued.Verifier,
			ownershipVerifier(proof), owner.Generation, func() time.Time { return at })
		assertIncompatibleAttemptDenied(t, f.databaseURL, created.Agent.ID, err, ErrAgentOwnershipDenied, 1, 1, 1, 0)
	})
}

func assertIncompatibleAttemptDenied(t *testing.T, databaseURL, agentID string, got, want error, owners, grants, configurations, attempts int) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("incompatible attempt error=%v, want %v", got, want)
	}
	conn := integrationConnect(t, databaseURL)
	var ownerCount, grantCount, configCount, attemptCount int
	if err := conn.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_configurations WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_incompatible_attempts WHERE agent_id=$1)`, agentID).Scan(&ownerCount, &grantCount, &configCount, &attemptCount); err != nil {
		t.Fatal("read denied incompatible attempt state failed")
	}
	closeIntegrationConn(conn)
	if ownerCount != owners || grantCount != grants || configCount != configurations || attemptCount != attempts {
		t.Fatalf("denied incompatible owner/grant/config/attempt counts=%d/%d/%d/%d, want %d/%d/%d/%d",
			ownerCount, grantCount, configCount, attemptCount, owners, grants, configurations, attempts)
	}
}
