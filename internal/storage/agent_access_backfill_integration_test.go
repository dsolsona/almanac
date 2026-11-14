//go:build integration

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationAgentAccessContinuesDuringHistoricalObservationWrite(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "access-backfill", true)
	var credentialID, agentID, leaseID string
	var verifierBytes []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT c.id,c.agent_id,c.verifier,o.lease_id
FROM public.almanac_agent_credentials c JOIN public.almanac_sli_observations o ON o.agent_id=c.agent_id
WHERE o.sli_id=$1 LIMIT 1`, s.sliID).Scan(&credentialID, &agentID, &verifierBytes, &leaseID); err != nil {
		t.Fatal(err)
	}
	if len(verifierBytes) != 32 {
		t.Fatal("invalid fixture verifier length")
	}
	var verifier [32]byte
	copy(verifier[:], verifierBytes)
	writer, err := s.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(writer)
	// A real canonical INSERT holds foreign-key references until commit. The new
	// interval is irrelevant to credential authority and need not block access.
	if _, err := writer.Exec(t.Context(), `INSERT INTO public.almanac_sli_observations
SELECT (jsonb_populate_record(NULL::public.almanac_sli_observations,
to_jsonb(o)||jsonb_build_object('interval_start',$2::timestamptz,'execution_id',gen_random_uuid(),'delivery_id',gen_random_uuid()))).*
FROM public.almanac_sli_observations o WHERE o.sli_id=$1 LIMIT 1`, s.sliID, s.at); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	principal, err := AuthenticateAgentCredential(ctx, s.f.databaseURL, credentialID, verifier, s.clock, s.runtime)
	if err != nil || principal.AgentID != agentID {
		t.Fatalf("historical foreign-key reference blocked current agent authority: %v", err)
	}
	conn, err := s.runtime.Acquire(ctx, LiveTraffic)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	access, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(access)
	q := dbgen.New(access)
	if _, err := q.GetAgentExecutionGrantByLease(ctx, leaseID); err != nil {
		t.Fatalf("historical reference blocked execution grant validation: %v", err)
	}
	if _, err := q.LockSliMeasurementForObservation(ctx, dbgen.LockSliMeasurementForObservationParams{
		SliID: mustPGUUID(s.sliID), Revision: 1, IntervalStart: timestamp(s.at.Add(-s.cadence)),
	}); err != nil {
		t.Fatalf("historical reference blocked measurement validation: %v", err)
	}
}
