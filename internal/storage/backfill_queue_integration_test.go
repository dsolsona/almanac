//go:build integration

package storage

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
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
	first, err := RequestBackfill(t.Context(), s.f.databaseURL, input)
	if err != nil || first.ID == "" || first.ChunkCount != 1 {
		t.Fatalf("admission = %+v, %v", first, err)
	}
	replay, err := RequestBackfill(t.Context(), s.f.databaseURL, input)
	if err != nil || replay != first {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, first)
	}
	input.RequestKey = "overlap"
	if _, err := RequestBackfill(t.Context(), s.f.databaseURL, input); !errors.Is(err, ErrBackfillOverlap) {
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
    capabilities='[{"provider":"prometheus","operations":["backfill"]}]'
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
	for i := range 3 {
		if err := s.runtime.SweepBackfill(ctx, s.at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
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
