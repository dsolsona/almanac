//go:build integration

package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationBackfillClaimExpiryAndCancellation(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-claim", true)
	ctx := t.Context()
	var id, agentID string
	err := s.conn.QueryRow(ctx, `
INSERT INTO public.almanac_backfill_jobs
(sli_id, requested_start, requested_end, request_key, requested_by, created_at)
SELECT $1, $2, $3, 'test-request', u.id, $3 FROM public.almanac_users u LIMIT 1
RETURNING id, (SELECT agent_id FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1)`,
		s.sliID, s.historyStart, s.at).Scan(&id, &agentID)
	if err != nil {
		t.Fatal(err)
	}
	var chunkID string
	err = s.conn.QueryRow(ctx, `
INSERT INTO public.almanac_backfill_chunks
(job_id, sli_id, measurement_revision, agent_id, range_start, range_end, cadence_seconds, next_attempt_at)
VALUES($1,$2,1,$3,$4,$5,60,$5) RETURNING id`,
		id, s.sliID, agentID, s.historyStart, s.at).Scan(&chunkID)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(now time.Time) (int64, error) {
		var generation int64
		err := s.conn.QueryRow(ctx, `SELECT generation FROM public.almanac_claim_backfill_chunk($1,$2)`, agentID, now).Scan(&generation)
		return generation, err
	}
	first, err := claim(s.at)
	if err != nil || first != 1 {
		t.Fatalf("first claim = %d, %v", first, err)
	}
	if _, err := claim(s.at.Add(time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("leased chunk was claimable: %v", err)
	}
	if _, err := s.conn.Exec(ctx, `SELECT public.almanac_reap_backfill_chunks($1)`, s.at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := claim(s.at.Add(2 * time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retry backoff was skipped: %v", err)
	}
	second, err := claim(s.at.Add(3 * time.Minute))
	if err != nil || second <= first {
		t.Fatalf("replacement claim = %d, %v; previous = %d", second, err, first)
	}
	finish := func(generation int64) (bool, error) {
		var accepted bool
		err := s.conn.QueryRow(ctx, `SELECT public.almanac_finish_backfill_chunk($1,$2,$3,$4,'committed',NULL)`,
			chunkID, agentID, generation, s.at.Add(3*time.Minute)).Scan(&accepted)
		return accepted, err
	}
	if ok, err := finish(first); ok || err != nil {
		t.Fatalf("expired claim finished: %v, %v", ok, err)
	}
	if _, err := s.conn.Exec(ctx, `SELECT public.almanac_cancel_backfill_job($1,$2)`, id, s.at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := finish(second); ok || err != nil {
		t.Fatalf("cancelled claim finished: %v, %v", ok, err)
	}
	var state string
	var lease sql.NullTime
	if err := s.conn.QueryRow(ctx, `SELECT state, lease_until FROM public.almanac_backfill_chunks WHERE id=$1`, chunkID).Scan(&state, &lease); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || lease.Valid {
		t.Fatalf("cancelled chunk = %q, lease %v", state, lease)
	}
}

func TestIntegrationBackfillAdmissionIsAlignedAndIdempotent(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-admit", true)
	input := BackfillRequest{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier},
		SLIID: s.sliID, Start: s.historyStart, End: s.at.Add(-5 * time.Minute),
		RequestKey: "manual-backfill", Now: s.clock,
	}
	if _, err := s.conn.Exec(t.Context(), `DELETE FROM public.almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2`, s.sliID, input.Start); err != nil {
		t.Fatal(err)
	}
	unexpectedContext := input
	unexpectedContext.EarlierHistoryContext = "unused"
	for _, preview := range []bool{true, false} {
		unexpectedContext.Preview = preview
		if _, err := RequestBackfill(t.Context(), s.f.databaseURL, unexpectedContext); !errors.Is(err, ErrSliInvalid) {
			t.Fatalf("ordinary request with context (preview=%t) returned %v", preview, err)
		}
	}
	preview := input
	preview.Preview = true
	planned, err := RequestBackfill(t.Context(), s.f.databaseURL, preview)
	if err != nil || planned.Plan == nil || planned.Plan.Eligible != 1 || planned.Plan.Covered != 174 || planned.ChunkCount != 1 {
		t.Fatalf("preview = %+v, plan %+v, err %v", planned, planned.Plan, err)
	}
	first, err := RequestBackfill(t.Context(), s.f.databaseURL, input)
	if err != nil || first.ID == "" || first.ChunkCount != 1 {
		t.Fatalf("admission = %+v, %v", first, err)
	}
	replay, err := RequestBackfill(t.Context(), s.f.databaseURL, input)
	if err != nil || replay != first {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, first)
	}
	planned, err = RequestBackfill(t.Context(), s.f.databaseURL, preview)
	if err != nil || planned.Plan == nil || planned.Plan.Manual != 1 {
		t.Fatalf("preview with an existing admission key = %+v, %v", planned, err)
	}
	input.RequestKey = "overlap"
	if _, err := RequestBackfill(t.Context(), s.f.databaseURL, input); !errors.Is(err, ErrBackfillNoGaps) {
		t.Fatalf("overlap = %v", err)
	}
	input.Start = input.Start.Add(time.Second)
	if _, err := RequestBackfill(t.Context(), s.f.databaseURL, input); !errors.Is(err, ErrBackfillRange) {
		t.Fatalf("unaligned request = %v", err)
	}
}

func TestIntegrationBackfillCommitUpdatesDerivedObservationGeneration(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-commit", true)
	ctx := t.Context()
	at := s.historyStart
	if _, err := s.conn.Exec(ctx, `DELETE FROM public.almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2`, s.sliID, at); err != nil {
		t.Fatal(err)
	}
	admitted, err := RequestBackfill(ctx, s.f.databaseURL, BackfillRequest{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier}, SLIID: s.sliID,
		Start: at, End: at.Add(time.Minute), RequestKey: "fill-gap", Now: s.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	var agentID, chunkID string
	var generation int64
	if err := s.conn.QueryRow(ctx, `SELECT agent_id FROM public.almanac_backfill_chunks WHERE job_id=$1`, admitted.ID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if err := s.conn.QueryRow(ctx, `SELECT id, generation FROM public.almanac_claim_backfill_chunk($1,$2)`, agentID, s.at).Scan(&chunkID, &generation); err != nil {
		t.Fatal(err)
	}
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	result := sli.BackfillChunkResult{
		ChunkID: chunkID, Generation: generation,
		Outcomes: []sli.ExecutionOutcome{{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}},
	}
	if err := commitBackfillChunk(ctx, tx, agentID, 1, 1, s.at, result); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var good, total string
	var sourceGeneration int64
	var dirty bool
	err = s.conn.QueryRow(ctx, `
SELECT o.good_count::text, o.total_count::text, o.ingestion_generation,
       EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d WHERE d.sli_id=o.sli_id
              AND d.range_start <= o.interval_start AND o.interval_start < d.range_end
              AND d.generation >= o.ingestion_generation)
FROM public.almanac_sli_observations o WHERE o.sli_id=$1 AND o.interval_start=$2`, s.sliID, at).
		Scan(&good, &total, &sourceGeneration, &dirty)
	if err != nil || good != "99.000000000000" || total != "100.000000000000" || sourceGeneration == 0 || !dirty {
		t.Fatalf("observation = %s/%s generation=%d dirty=%v err=%v", good, total, sourceGeneration, dirty, err)
	}
}

func TestIntegrationBackfillSweepQueuesOverdueGapOnce(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-sweep", true)
	ctx := t.Context()
	first := s.historyStart.Add(time.Minute)
	knownEmpty := first.Add(time.Minute)
	if _, err := s.conn.Exec(ctx, `
UPDATE public.almanac_sli_measurements SET activated_at=$2 WHERE sli_id=$1`, s.sliID, s.historyStart); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `
UPDATE public.almanac_agent_runtime_status
SET last_heartbeat_at=$2, applied_configuration_revision=desired_configuration_revision,
    capabilities='[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["collect","backfill"]}]'
WHERE agent_id=(SELECT agent_id FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1)`, s.sliID, s.at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `
DELETE FROM public.almanac_sli_observations WHERE sli_id=$1 AND interval_start IN ($2,$3)`, s.sliID, first, s.at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `
UPDATE public.almanac_sli_observations SET outcome='complete_empty',good_count=NULL,total_count=NULL
WHERE sli_id=$1 AND interval_start=$2`, s.sliID, knownEmpty); err != nil {
		t.Fatal(err)
	}
	var datasourceID string
	if err := s.conn.QueryRow(ctx, `SELECT datasource_id FROM almanac_sli_measurements WHERE sli_id=$1`, s.sliID).Scan(&datasourceID); err != nil {
		t.Fatal(err)
	}
	second, err := CreateSli(ctx, s.f.databaseURL, CreateSliInput{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier}, Name: "second-sweep", DisplayName: "Second SLI",
		DatasourceID: datasourceID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount,
		GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "second-sweep", RequestID: "second-sweep", Now: s.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_sli_measurements SET effective_from=$2, activated_at=$2 WHERE sli_id=$1`, second.ID, s.at.Add(-12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_agent_runtime_status SET applied_configuration_revision=desired_configuration_revision`); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := s.runtime.SweepBackfill(ctx, s.at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		var discovered int
		if err := s.conn.QueryRow(ctx, `SELECT count(DISTINCT sli_id) FROM almanac_backfill_jobs WHERE requested_by IS NULL`).Scan(&discovered); err != nil || discovered != 2 {
			t.Fatalf("sweep did not reach both SLIs within the interval budget: %d, %v", discovered, err)
		}
	}
	var jobs int
	var start, end time.Time
	if err := s.conn.QueryRow(ctx, `
SELECT count(*),min(requested_start),max(requested_end) FROM public.almanac_backfill_jobs
WHERE sli_id=$1 AND requested_by IS NULL`, s.sliID).Scan(&jobs, &start, &end); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || !start.Equal(first) || !end.Equal(knownEmpty) {
		t.Fatalf("automatic jobs=%d range=[%s, %s), want one gap [%s, %s)", jobs, start, end, first, knownEmpty)
	}
}

func TestIntegrationBackfillPreviewUsesDatasourceRetention(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-retention", true)
	ctx := t.Context()
	start := s.at.Add(-8 * 24 * time.Hour)
	if _, err := s.conn.Exec(ctx,
		`UPDATE public.almanac_sli_measurements SET effective_from=$2 WHERE sli_id=$1`, s.sliID, start); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `
UPDATE public.almanac_datasources SET historical_retention_days=7
WHERE id=(SELECT datasource_id FROM public.almanac_sli_measurements WHERE sli_id=$1)`, s.sliID); err != nil {
		t.Fatal(err)
	}
	preview, err := RequestBackfill(ctx, s.f.databaseURL, BackfillRequest{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier}, SLIID: s.sliID,
		Start: start, End: s.at.Add(-5 * time.Minute), RequestKey: "preview-retention", Preview: true, Now: s.clock,
	})
	if err != nil || preview.Plan == nil {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	plan := preview.Plan
	if plan.RetentionDays != 7 || plan.OutsideRetention != 1440 || plan.Automatic != 0 ||
		!plan.RecoveryStart.Equal(s.at.Add(-7*24*time.Hour)) || plan.Covered != 175 {
		t.Fatalf("retention plan: %+v", plan)
	}
	if plan.Eligible != 9900 || plan.Chunks != 155 {
		t.Fatalf("eligible 9900 intervals in 155 chunks, got %+v", plan)
	}
}

func TestIntegrationBackfillKeepsPartialResultsAndRetryBudget(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-partial", true)
	ctx := t.Context()
	start := s.historyStart
	end := start.Add(3 * time.Minute)
	if _, err := s.conn.Exec(ctx, `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start >= $2 AND interval_start < $3`, s.sliID, start, end); err != nil {
		t.Fatal(err)
	}
	input := BackfillRequest{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier}, SLIID: s.sliID,
		Start: start, End: end, RequestKey: "partial", Now: s.clock,
	}
	admission, err := RequestBackfill(ctx, s.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	var agentID string
	if err := s.conn.QueryRow(ctx, `SELECT agent_id FROM almanac_backfill_chunks WHERE job_id=$1`, admission.ID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	observed := sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}
	missing := sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: "incomplete"}
	for attempt := range 3 {
		now := s.at.Add(time.Duration(attempt) * 3 * time.Minute)
		var result sli.BackfillChunkResult
		if err := s.conn.QueryRow(ctx, `SELECT id,generation FROM almanac_claim_backfill_chunk($1,$2)`, agentID, now).Scan(&result.ChunkID, &result.Generation); err != nil {
			t.Fatal(err)
		}
		result.Outcomes = []sli.ExecutionOutcome{{}, missing, {}}
		if attempt == 0 {
			result.Outcomes[0], result.Outcomes[2] = observed, observed
			result.Outcomes[1].ErrorCode = "rate_limited"
			result.RetryAfterSeconds = 120
		}
		tx, err := s.conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := commitBackfillChunk(ctx, tx, agentID, 1, 1, now, result); err != nil {
			rollbackTransaction(tx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			var next time.Time
			if err := s.conn.QueryRow(ctx, `SELECT next_attempt_at FROM almanac_backfill_chunks WHERE id=$1`, result.ChunkID).Scan(&next); err != nil || !next.Equal(now.Add(2*time.Minute)) {
				t.Fatalf("provider Retry-After was not persisted: %s, %v", next, err)
			}
		}
	}
	jobs, err := ListBackfills(ctx, s.f.databaseURL, input.Proof, s.sliID, s.clock)
	if err != nil || len(jobs) != 1 || jobs[0].RecoveredIntervals != 2 || jobs[0].RequestedIntervals != 3 || jobs[0].State != "completed-with-gaps" {
		t.Fatalf("partial job = %+v, %v", jobs, err)
	}
	discover := func() {
		t.Helper()
		tx, err := s.conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackTransaction(tx)
		if err := enqueueDiscoveredGaps(ctx, tx, s.sliID, 1, agentID, start, end, s.at, 60); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	discover()
	input.RequestKey, input.Preview = "inspect-exhausted", true
	preview, err := RequestBackfill(ctx, s.f.databaseURL, input)
	if err != nil || preview.Plan == nil || preview.Plan.PreviousFailure != 1 || preview.Plan.Covered != 2 {
		t.Fatalf("exhausted preview = %+v, %v", preview.Plan, err)
	}
	input.RetryExhausted = true
	preview, err = RequestBackfill(ctx, s.f.databaseURL, input)
	if err != nil || preview.Plan == nil || preview.Plan.Eligible != 1 {
		t.Fatalf("explicit retry preview = %+v, %v", preview.Plan, err)
	}
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_datasources SET historical_configuration_version=historical_configuration_version+1
WHERE id=(SELECT datasource_id FROM almanac_sli_measurements WHERE sli_id=$1 AND revision=1)`, s.sliID); err != nil {
		t.Fatal(err)
	}
	discover()
	jobs, err = ListBackfills(ctx, s.f.databaseURL, input.Proof, s.sliID, s.clock)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("expected one automatic cycle after datasource change, got %+v, %v", jobs, err)
	}
}

func TestIntegrationBackfillFencesOwnerAndTrimsMovingRetention(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-owner-retention", true)
	ctx := t.Context()
	start := s.at.Add(-7 * 24 * time.Hour)
	end := start.Add(3 * time.Minute)
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_sli_measurements SET effective_from=$2 WHERE sli_id=$1`, s.sliID, start); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_datasources SET historical_retention_days=7
WHERE id=(SELECT datasource_id FROM almanac_sli_measurements WHERE sli_id=$1)`, s.sliID); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestBackfill(ctx, s.f.databaseURL, BackfillRequest{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier}, SLIID: s.sliID, Start: start, End: end,
		RequestKey: "retention-moving", Now: s.clock,
	}); err != nil {
		t.Fatal(err)
	}
	var proof BackfillAgentProof
	var credentialVerifier []byte
	err := s.conn.QueryRow(ctx, `SELECT c.id,c.verifier FROM almanac_agent_credentials c
JOIN almanac_sli_measurements m ON m.agent_id=c.agent_id WHERE m.sli_id=$1`, s.sliID).Scan(&proof.CredentialID, &credentialVerifier)
	if err != nil {
		t.Fatal(err)
	}
	copy(proof.Verifier[:], credentialVerifier)
	proof.ResumeVerifier = ownershipVerifier(ownershipBytes(32, 82))
	s.now = s.at.Add(time.Second)
	owner, err := AcquireAgentOwnership(ctx, s.f.databaseURL, proof.CredentialID, proof.Verifier, proof.ResumeVerifier, s.clock)
	if err != nil {
		t.Fatal(err)
	}
	proof.Generation = owner.Generation
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_agent_runtime_status SET generation=$2,
 last_heartbeat_at=$3, scheduler_health='ready', applied_configuration_revision=desired_configuration_revision
 WHERE agent_id=(SELECT agent_id FROM almanac_sli_measurements WHERE sli_id=$1)`, s.sliID, proof.Generation, s.now); err != nil {
		t.Fatal(err)
	}
	stale := proof
	stale.ResumeVerifier = ownershipVerifier(ownershipBytes(32, 81))
	if _, err := ClaimBackfill(ctx, s.f.databaseURL, stale, s.clock, s.runtime); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("claim with previous owner's proof: %v", err)
	}
	claim, err := ClaimBackfill(ctx, s.f.databaseURL, proof, s.clock, s.runtime)
	if err != nil || claim.ID == "" || !claim.Start.Equal(start.Add(time.Minute)) || !claim.End.Equal(end) {
		t.Fatalf("retained suffix = %+v, %v", claim, err)
	}
	observed := sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}
	result := sli.BackfillChunkResult{ChunkID: claim.ID, Generation: claim.Generation, Outcomes: []sli.ExecutionOutcome{observed, observed}}
	s.now = s.now.Add(time.Minute)
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_agent_runtime_status SET last_heartbeat_at=$2
 WHERE agent_id=(SELECT agent_id FROM almanac_sli_measurements WHERE sli_id=$1)`, s.sliID, s.now); err != nil {
		t.Fatal(err)
	}
	if err := SubmitBackfill(ctx, s.f.databaseURL, stale, s.clock, result, s.runtime); !errors.Is(err, ErrObservationDenied) {
		t.Fatalf("submission with previous owner's proof: %v", err)
	}
	if err := SubmitBackfill(ctx, s.f.databaseURL, proof, s.clock, result, s.runtime); err != nil {
		t.Fatal(err)
	}
	var recovered []time.Time
	if err := s.conn.QueryRow(ctx, `SELECT array_agg(interval_start) FROM almanac_sli_observations WHERE backfill_chunk_id=$1`, claim.ID).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || !recovered[0].Equal(end.Add(-time.Minute)) {
		t.Fatalf("recovered intervals = %v", recovered)
	}
}

func TestIntegrationBackfillFullChunkAlongsideLiveIngestion(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-live", true)
	ctx := t.Context()
	start := s.historyStart
	end := start.Add(64 * time.Minute)
	liveStart := s.at.Add(-3 * time.Minute)
	if _, err := s.conn.Exec(ctx, `DELETE FROM almanac_sli_observations WHERE sli_id=$1
AND ((interval_start >= $2 AND interval_start < $3) OR interval_start=$4)`, s.sliID, start, end, liveStart); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestBackfill(ctx, s.f.databaseURL, BackfillRequest{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier}, SLIID: s.sliID,
		Start: start, End: end, RequestKey: "full-chunk", Now: s.clock,
	}); err != nil {
		t.Fatal(err)
	}
	var proof BackfillAgentProof
	var credentialVerifier []byte
	if err := s.conn.QueryRow(ctx, `SELECT c.id,c.verifier FROM almanac_agent_credentials c
JOIN almanac_sli_measurements m ON m.agent_id=c.agent_id WHERE m.sli_id=$1`, s.sliID).Scan(&proof.CredentialID, &credentialVerifier); err != nil {
		t.Fatal(err)
	}
	copy(proof.Verifier[:], credentialVerifier)
	proof.ResumeVerifier = ownershipVerifier(ownershipBytes(32, 82))
	owner, err := AcquireAgentOwnership(ctx, s.f.databaseURL, proof.CredentialID, proof.Verifier, proof.ResumeVerifier, s.clock)
	if err != nil {
		t.Fatal(err)
	}
	proof.Generation = owner.Generation
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_agent_runtime_status SET generation=$2,
 last_heartbeat_at=$3, scheduler_health='ready', applied_configuration_revision=desired_configuration_revision
 WHERE agent_id=$1`, owner.AgentID, proof.Generation, s.now); err != nil {
		t.Fatal(err)
	}
	allowed, err := json.Marshal([]agentprotocol.MeasurementGrant{{SLIID: s.sliID, MeasurementRevision: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `UPDATE almanac_agent_execution_grants SET allowed_measurements=$1 WHERE lease_id=$2`, allowed, owner.LeaseID); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimBackfill(ctx, s.f.databaseURL, proof, s.clock, s.runtime)
	if err != nil || claim.ID == "" || claim.End.Sub(claim.Start) != 64*time.Minute {
		t.Fatalf("full claim = %+v, %v", claim, err)
	}
	observed := sli.ExecutionOutcome{State: sli.OutcomeObserved, GoodCount: "99", TotalCount: "100"}
	result := sli.BackfillChunkResult{ChunkID: claim.ID, Generation: claim.Generation, Outcomes: make([]sli.ExecutionOutcome, 64)}
	for index := range result.Outcomes {
		result.Outcomes[index] = observed
	}
	item := sli.ObservationItem{DeliveryID: uuid.NewString(), ExecutionID: uuid.NewString(), CreatedAt: s.now, LeaseID: owner.LeaseID,
		ConfigurationRevision: strconv.FormatInt(owner.ConfigurationRevision, 10), SessionGeneration: strconv.FormatInt(owner.Generation, 10),
		SLIID: s.sliID, MeasurementRevision: "1", IntervalStart: liveStart, IntervalEnd: liveStart.Add(time.Minute), Outcome: observed}
	state, now, revision, err := beginBackfillAgent(ctx, s.f.databaseURL, proof, s.clock, s.runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAgentOwnershipTransaction(state)
	live := make(chan error, 1)
	started := time.Now()
	go func() {
		accepted, err := IngestAgentObservation(ctx, s.f.databaseURL, proof.CredentialID, proof.Verifier, item, s.clock, s.runtime)
		if err == nil && accepted.Status != "accepted" {
			err = errors.New("live observation status: " + accepted.Status + " " + accepted.ErrorCode)
		}
		live <- err
	}()
	waitForAgentQueryWait(t, s.conn, "%FROM public.almanac_agents%")
	fenced := time.Now()
	if err := commitBackfillChunk(ctx, state.tx, owner.AgentID, proof.Generation, revision, now, result); err != nil {
		t.Fatal(err)
	}
	if err := state.tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	committed := time.Now()
	if err := <-live; err != nil {
		t.Fatalf("concurrent live observation: %v", err)
	}
	t.Logf("64 intervals committed in %s. Concurrent live acceptance took %s including the observed agent-lock wait.", committed.Sub(fenced), time.Since(started))
	var rows int
	if err := s.conn.QueryRow(ctx, `SELECT count(*) FROM almanac_sli_observations
 WHERE backfill_chunk_id=$1 OR delivery_id=$2`, claim.ID, item.DeliveryID).Scan(&rows); err != nil || rows != 65 {
		t.Fatalf("committed rows = %d, %v", rows, err)
	}
}

func TestIntegrationSetupBackfillExtendsOnlyConfirmedInitialHistory(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-setup", true)
	ctx := t.Context()
	input := BackfillRequest{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier},
		SLIID: s.sliID, Start: s.historyStart.Add(-28 * 24 * time.Hour), End: s.historyStart,
		RequestKey: "earlier-history", Now: s.clock, Preview: true,
	}
	if ordinary, err := RequestBackfill(ctx, s.f.databaseURL, input); err != nil || ordinary.ChunkCount != 0 {
		t.Fatalf("ordinary preview = %+v, %v", ordinary, err)
	}
	input.IncludeEarlierHistory = true
	if _, err := RequestBackfill(ctx, s.f.databaseURL, input); !errors.Is(err, ErrBackfillEarlierUnavailable) {
		t.Fatalf("unactivated original definition returned %v", err)
	}
	if _, err := s.conn.Exec(ctx, `UPDATE public.almanac_sli_measurements SET activated_at=$2 WHERE sli_id=$1 AND revision=1`, s.sliID, s.historyStart); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestBackfill(ctx, s.f.databaseURL, input); !errors.Is(err, ErrBackfillEarlierUnavailable) {
		t.Fatalf("agent without historical capability returned %v", err)
	}
	if _, err := s.conn.Exec(ctx, `
UPDATE public.almanac_agent_runtime_status
SET capabilities=jsonb_set(capabilities,'{0,operations}',(capabilities#>'{0,operations}')||'["backfill"]'::jsonb)
WHERE agent_id=(SELECT agent_id FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1)`, s.sliID); err != nil {
		t.Fatal(err)
	}
	disjoint := input
	disjoint.End = s.historyStart.Add(-time.Minute)
	if _, err := RequestBackfill(ctx, s.f.databaseURL, disjoint); !errors.Is(err, ErrBackfillEarlierRange) {
		t.Fatalf("disjoint earlier selection returned %v", err)
	}
	setRetention := func(days int) {
		t.Helper()
		if _, err := s.conn.Exec(ctx, `
UPDATE public.almanac_datasources SET historical_retention_days=$2
WHERE id=(SELECT datasource_id FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1)`, s.sliID, days); err != nil {
			t.Fatal(err)
		}
	}
	setRetention(7)
	clipped, err := RequestBackfill(ctx, s.f.databaseURL, input)
	if err != nil || clipped.Plan == nil || clipped.Plan.Eligible != 9900 || clipped.Plan.OutsideRetention != 30420 ||
		!clipped.EarlierHistoryFrom.Equal(s.at.Add(-7*24*time.Hour)) {
		t.Fatalf("retention-clipped setup preview = %+v, plan %+v, %v", clipped, clipped.Plan, err)
	}
	setRetention(0)
	preview, err := RequestBackfill(ctx, s.f.databaseURL, input)
	if err != nil || preview.Plan == nil || preview.Plan.Eligible != 28*1440 || preview.ChunkCount != 630 {
		t.Fatalf("setup preview = %+v, plan %+v, %v", preview, preview.Plan, err)
	}
	var before, after time.Time
	readValidity := func(value *time.Time) {
		t.Helper()
		if err := s.conn.QueryRow(ctx, `SELECT effective_from FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1`, s.sliID).Scan(value); err != nil {
			t.Fatal(err)
		}
	}
	readValidity(&before)
	if !before.Equal(s.historyStart) {
		t.Fatalf("preview changed validity to %s", before)
	}
	input.Preview = false
	input.EarlierHistoryContext = preview.EarlierHistoryContext
	if _, err := s.conn.Exec(ctx, `
UPDATE public.almanac_datasources SET historical_configuration_version=historical_configuration_version+1
WHERE id=(SELECT datasource_id FROM public.almanac_sli_measurements WHERE sli_id=$1 AND revision=1)`, s.sliID); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestBackfill(ctx, s.f.databaseURL, input); !errors.Is(err, ErrBackfillEarlierChanged) {
		t.Fatalf("confirmation with stale datasource configuration returned %v", err)
	}
	input.Preview = true
	input.EarlierHistoryContext = ""
	preview, err = RequestBackfill(ctx, s.f.databaseURL, input)
	if err != nil || preview.EarlierHistoryContext == "" {
		t.Fatalf("fresh preview = %+v, %v", preview, err)
	}
	input.Preview = false
	input.EarlierHistoryContext = preview.EarlierHistoryContext
	admitted, err := RequestBackfill(ctx, s.f.databaseURL, input)
	if err != nil || admitted.ID == "" || admitted.ChunkCount != 630 {
		t.Fatalf("setup admission = %+v, %v", admitted, err)
	}
	readValidity(&after)
	if !after.Equal(input.Start) {
		t.Fatalf("validity = %s, want %s", after, input.Start)
	}
	var liveFrom, authorizedFrom, authorizedBefore time.Time
	if err := s.conn.QueryRow(ctx, `
SELECT m.initial_live_from,j.earlier_history_from,j.previous_valid_from
FROM public.almanac_backfill_jobs j
JOIN public.almanac_sli_measurements m ON m.sli_id=j.sli_id AND m.revision=1
WHERE j.id=$1 AND j.requested_by IS NOT NULL`, admitted.ID).Scan(&liveFrom, &authorizedFrom, &authorizedBefore); err != nil {
		t.Fatal(err)
	}
	if !liveFrom.Equal(s.historyStart) || !authorizedBefore.Equal(s.historyStart) || !authorizedFrom.Equal(input.Start) {
		t.Fatalf("live boundary %s, authorized range %s to %s", liveFrom, authorizedFrom, authorizedBefore)
	}
	replay, err := RequestBackfill(ctx, s.f.databaseURL, input)
	if err != nil || replay != admitted {
		t.Fatalf("setup replay = %+v, %v", replay, err)
	}
	input.IncludeEarlierHistory = false
	input.EarlierHistoryContext = ""
	if _, err := RequestBackfill(ctx, s.f.databaseURL, input); !errors.Is(err, ErrSliConflict) {
		t.Fatalf("changed request keyed as earlier history returned %v", err)
	}
}

func TestIntegrationBackfillRetryAfterPausesOtherDatasourceChunks(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "backfill-cooldown", true)
	ctx := t.Context()
	if _, err := s.conn.Exec(ctx, `DELETE FROM public.almanac_sli_observations WHERE sli_id=$1 AND interval_start < $2`, s.sliID, s.historyStart.Add(65*time.Minute)); err != nil {
		t.Fatal(err)
	}
	admission, err := RequestBackfill(ctx, s.f.databaseURL, BackfillRequest{
		Proof: DatasourceProof{SessionVerifier: s.f.verifier}, SLIID: s.sliID,
		Start: s.historyStart, End: s.historyStart.Add(65 * time.Minute), RequestKey: "cooldown", Now: s.clock,
	})
	if err != nil || admission.ChunkCount != 2 {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	var agentID string
	if err := s.conn.QueryRow(ctx, `SELECT agent_id FROM public.almanac_backfill_chunks WHERE job_id=$1 LIMIT 1`, admission.ID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	claim := func(at time.Time) (sli.BackfillChunkResult, error) {
		var result sli.BackfillChunkResult
		err := s.conn.QueryRow(ctx, `SELECT id,generation FROM public.almanac_claim_backfill_chunk($1,$2)`, agentID, at).Scan(&result.ChunkID, &result.Generation)
		return result, err
	}
	first, err := claim(s.at)
	if err != nil {
		t.Fatal(err)
	}
	first.RetryAfterSeconds = 120
	first.Outcomes = make([]sli.ExecutionOutcome, 64)
	for i := range first.Outcomes {
		first.Outcomes[i] = sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: "rate_limited"}
	}
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if err := commitBackfillChunk(ctx, tx, agentID, 1, 1, s.at, first); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := claim(s.at.Add(time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another chunk ignored the datasource cooldown: %v", err)
	}
	second, err := claim(s.at.Add(2 * time.Minute))
	if err != nil || second.ChunkID == first.ChunkID {
		t.Fatalf("queued chunk did not resume after cooldown: %+v, %v", second, err)
	}
}
