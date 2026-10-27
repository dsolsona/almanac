//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
)

func TestIntegrationAgentOwnershipAndEmptyExecutionGrantCommitTogether(t *testing.T) {
	f := newAgentFixture(t)
	issued, created := createOwnershipAgent(t, f, "runtime-grant")
	proof := ownershipBytes(32, 71)
	now := f.base.Add(4 * time.Second)
	clock := func() time.Time { return now }
	first, err := AcquireAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), clock)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != 1 || first.ConfigurationRevision != 1 || first.LeaseID == "" || len(first.AllowedMeasurements) != 0 {
		t.Fatalf("initial execution state = %+v", first)
	}
	var snapshot agentprotocol.ConfigurationSnapshot
	if err := json.Unmarshal(first.ConfigurationSnapshot, &snapshot); err != nil || snapshot.Assignments == nil || len(snapshot.Assignments) != 0 {
		t.Fatalf("initial snapshot assignments = %s, %v", first.ConfigurationSnapshot, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var ownerCount, grantCount, configCount int
	if err := conn.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1 AND generation=1),
		(SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1 AND generation=1),
		(SELECT count(*) FROM public.almanac_agent_configurations WHERE agent_id=$1 AND revision=1)`, created.Agent.ID).Scan(&ownerCount, &grantCount, &configCount); err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(conn)
	if ownerCount != 1 || grantCount != 1 || configCount != 1 {
		t.Fatalf("durable owner/grant/config counts = %d/%d/%d", ownerCount, grantCount, configCount)
	}

	nonce := ownershipNonce(72)
	now = now.Add(10 * time.Second)
	renewed, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 0, nonce, clock)
	if err != nil || renewed.RenewalSequence != 1 || renewed.LeaseID != first.LeaseID || renewed.ConfigurationRevision != first.ConfigurationRevision {
		t.Fatalf("renewed execution state = %+v, %v", renewed, err)
	}
	replay, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 0, nonce, clock)
	if err != nil || replay.RenewalSequence != 1 || replay.LeaseID != first.LeaseID || !replay.ExpiresAt.Equal(renewed.ExpiresAt) {
		t.Fatalf("renewal replay = %+v, %v", replay, err)
	}
	if _, err := RenewAgentOwnership(t.Context(), f.databaseURL, issued.ID, issued.Verifier, ownershipVerifier(proof), first.Generation, 0, ownershipNonce(73), clock); !errors.Is(err, ErrAgentOwnershipDenied) {
		t.Fatalf("stale renewal error = %v", err)
	}
}
