//go:build integration

package storage

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func verifyDerivedOperationalRecovery(t *testing.T, databaseURL string, proof DatasourceProof, id string, clock func() time.Time) {
	runtime, err := OpenRuntime(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	verifyReadinessAdmission(t, runtime)
	input := OperationalDetailInput{Proof: proof, ID: id, Now: clock, BurnWindow: 3600}
	oracle, err := ReadSloOperationalDetail(t.Context(), databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 100 {
		count, err := runtime.rebuildDerivedBatch(t.Context(), clock)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		if attempt == 99 {
			t.Fatal("fixture aggregate queue did not drain within its bound")
		}
	}
	pgID, _ := canonicalUUID(id)
	if err := runtime.refreshCurrentProjection(t.Context(), pgID, clock); err != nil {
		t.Fatalf("prepare aggregate current projection: %v", err)
	}
	status, err := ReadCurrentSloStatus(t.Context(), databaseURL, CurrentSloInput{Runtime: runtime, Proof: proof, ID: id, Now: clock})
	if err != nil || !reflect.DeepEqual(status.Current, oracle.Current) || !reflect.DeepEqual(status.Burn1h, oracle.Burn1h) || !reflect.DeepEqual(status.Burn6h, oracle.Burn6h) {
		t.Fatalf("aggregate current differs from canonical evaluator: %+v / %+v: %v", status, oracle.Current, err)
	}
	if status.Generation.EvaluatedThrough.Location() != time.UTC || status.Generation.PublishedAt.Location() != time.UTC {
		t.Fatal("current generation metadata must use UTC independently of the database driver's host timezone")
	}
	verifyRetainedCurrentPoint(t, runtime, databaseURL, proof, id, clock, oracle)
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var jobsBefore int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_history_jobs`).Scan(&jobsBefore); err != nil {
		t.Fatal(err)
	}
	input.Runtime = runtime
	if _, err := ReadSloOperationalDetail(t.Context(), databaseURL, input); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("explicit retained-history bootstrap was not distinguished: %v", err)
	}
	var jobsAfter int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_history_jobs`).Scan(&jobsAfter); err != nil || jobsAfter != jobsBefore {
		t.Fatalf("navigation admitted graph work during bootstrap: before=%d,after=%d,%v", jobsBefore, jobsAfter, err)
	}
	for attempt := range 1000 {
		worked, err := runtime.repairRetainedSeries(t.Context(), clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
		if attempt == 999 {
			t.Fatal("retained bootstrap did not drain its bounded fixture")
		}
	}
	for attempt := range 1000 {
		worked, err := runtime.prepareRetainedTile(t.Context(), clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
		if attempt == 999 {
			t.Fatal("retained tiles did not drain their bounded fixture")
		}
	}
	prepared, err := ReadSloOperationalDetail(t.Context(), databaseURL, input)
	if err != nil || prepared.Generation == nil {
		t.Fatalf("history was not published: %v", err)
	}
	verifyRetainedSelectedValues(t, databaseURL, proof, id, clock, prepared)
	if !reflect.DeepEqual(prepared.ComplianceSeries, oracle.ComplianceSeries) || !reflect.DeepEqual(prepared.ComplianceSummary, oracle.ComplianceSummary) {
		t.Fatal("retained completed-day compliance differs from canonical evaluation")
	}
	valuesInput := SloHistoryValuesInput{Runtime: runtime, Proof: proof, ID: id,
		GenerationID: prepared.Generation.GenerationId.String(), Lane: "budget", Limit: 3, Now: clock}
	first, err := ReadSloHistoryValues(t.Context(), databaseURL, valuesInput)
	if err != nil || !reflect.DeepEqual(first.Samples, prepared.BudgetSeries.Samples[:min(3, len(prepared.BudgetSeries.Samples))]) || first.Generation.GenerationId != prepared.Generation.GenerationId {
		t.Fatalf("exact values did not preserve the prepared generation: %+v, %v", first, err)
	}
	if first.NextOffset != nil {
		valuesInput.Offset = *first.NextOffset
		second, err := ReadSloHistoryValues(t.Context(), databaseURL, valuesInput)
		if err != nil || second.Generation.GenerationId != first.Generation.GenerationId || !reflect.DeepEqual(second.Samples, prepared.BudgetSeries.Samples[valuesInput.Offset:min(valuesInput.Offset+3, len(prepared.BudgetSeries.Samples))]) {
			t.Fatalf("exact pagination mixed generations or changed values: %v", err)
		}
	}
	valuesInput.Now = func() time.Time { return clock().Add(5*time.Minute + time.Microsecond) }
	if _, err := ReadSloHistoryValues(t.Context(), databaseURL, valuesInput); !errors.Is(err, ErrOperationalSnapshotOld) {
		t.Fatalf("expired values silently moved to another generation: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slo_history_blocks SET payload='{}' WHERE generation_id=$1`, pgtype.UUID{Bytes: prepared.Generation.GenerationId, Valid: true}); err != nil {
		t.Fatal(err)
	}
	repaired, err := ReadSloOperationalDetail(t.Context(), databaseURL, input)
	if err != nil || repaired.Generation == nil || repaired.Generation.GenerationId == prepared.Generation.GenerationId {
		t.Fatalf("corrupt response cache was not replaced directly from retained series: %v", err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_history_jobs`).Scan(&jobsAfter); err != nil || jobsAfter != jobsBefore {
		t.Fatalf("cold response-cache navigation queued graph work: %v", err)
	}
	// Explicit background jobs retain their own durable failure/retry contract;
	// their failure cannot block reads of independently maintained series.
	q := dbgen.New(conn)
	sourceGeneration, err := strconv.ParseInt(repaired.Generation.SourceGeneration, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.LockHistoryAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AdmitSloHistoryJob(t.Context(), dbgen.AdmitSloHistoryJobParams{SloID: pgID,
		DefinitionRevision: 1, MeasurementRevision: 1, SourceGeneration: sourceGeneration,
		CalculationVersion: operationalCalculationVersion, Kind: "detail", RetentionStart: timestamp(*repaired.Generation.RetentionStart),
		EvaluatedThrough: timestamp(repaired.EffectiveAt), RangeStart: timestamp(repaired.Range.RequestedStart),
		RangeEnd: timestamp(repaired.Range.RequestedEnd), BurnWindowSeconds: 3600, NextAttemptAt: timestamp(clock())}); err != nil {
		t.Fatal(err)
	}
	var jobID pgtype.UUID
	if err := conn.QueryRow(t.Context(), `UPDATE public.almanac_slo_history_jobs SET state='running',attempts=5,lease_until=$2 WHERE slo_id=$1 RETURNING id`, pgID, clock().Add(-time.Second)).Scan(&jobID); err != nil {
		t.Fatalf("claim replacement history for crash injection: %v", err)
	}
	if err := runtime.prepareNextHistory(t.Context(), clock); err != nil {
		t.Fatal(err)
	}
	state, err := ReadSloPreparation(t.Context(), databaseURL, CurrentSloInput{Runtime: runtime, Proof: proof, ID: id, Now: clock})
	if err != nil || len(state.Items) == 0 || state.Items[0].State != "failed" {
		t.Fatalf("last-attempt crash did not remain an explicit exhausted job: %v", err)
	}
	if _, err := ReadSloOperationalDetail(t.Context(), databaseURL, input); err != nil {
		t.Fatalf("failed explicit background job blocked maintained history: %v", err)
	}
	retry := RetrySloPreparationInput{Runtime: runtime, Proof: proof, SloID: id, JobID: jobID.String(), RequestID: "derived-manual-retry", Now: clock}
	if err := RetrySloPreparation(t.Context(), databaseURL, retry); err != nil {
		t.Fatal(err)
	}
	if err := RetrySloPreparation(t.Context(), databaseURL, retry); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("replayed retry reset the active budget: %v", err)
	}
	var auditCount int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_audit_events WHERE subject_id=$1 AND action='slo.history.retry' AND request_id=$2`, jobID, retry.RequestID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("admitted retry and its single audit did not commit together: count=%d, %v", auditCount, err)
	}
	if err := runtime.prepareNextHistory(t.Context(), clock); err != nil {
		t.Fatal(err)
	}
	verifyDerivedBlockFences(t, runtime, databaseURL, id, clock)
	repaired, err = ReadSloOperationalDetail(t.Context(), databaseURL, input)
	if err != nil || repaired.Generation == nil {
		t.Fatalf("history was unavailable after explicit job recovery: %v", err)
	}
	// A refreshed display name is not a new measurement/definition revision.
	if _, _, err := UpdateObjectiveMetadata(t.Context(), databaseURL, proof, id, "Renamed objective", "derived-rename", 1, clock, true); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slos SET display_name=$2,version=1,updated_at=$3 WHERE id=$1`, pgID, oracle.Objective.DisplayName, oracle.Objective.UpdatedAt); err != nil {
			t.Error(err)
		}
	}()
	renamed, err := ReadSloOperationalDetail(t.Context(), databaseURL, input)
	if err != nil || renamed.Objective.DisplayName != "Renamed objective" || renamed.Generation.GenerationId != repaired.Generation.GenerationId {
		t.Fatalf("cached history hid mutable display metadata: %v", err)
	}
	// Ensure failures cannot be retried forever merely because ingestion advances.
	for range 5 {
		if err := q.RecordCurrentPreparationFailure(t.Context(), dbgen.RecordCurrentPreparationFailureParams{SloID: pgID,
			DefinitionRevision: 1, MeasurementRevision: 1, CalculationVersion: operationalCalculationVersion, Column5: timestamp(clock())}); err != nil {
			t.Fatal(err)
		}
	}
	exhausted, err := q.CurrentPreparationExhausted(t.Context(), dbgen.CurrentPreparationExhaustedParams{SloID: pgID, CalculationVersion: operationalCalculationVersion})
	if err != nil || !exhausted {
		t.Fatalf("current failure budget was not durable: %v", err)
	}
	if err := q.ClearCurrentPreparationFailure(t.Context(), pgID); err != nil {
		t.Fatal(err)
	}
	verifyReadyOverviewReuse(t, runtime, databaseURL, proof, clock)
}

func verifyRetainedSelectedValues(t *testing.T, databaseURL string, proof DatasourceProof, id string, clock func() time.Time, value api.SloOperationalDetail) {
	t.Helper()
	canonical := make(map[time.Time]api.SloOperationalDetail)
	cadence := time.Duration(value.Objective.CadenceSeconds) * time.Second
	for name, samples := range map[string][]api.SloOperationalSample{
		"attainment": value.AttainmentSeries.Samples, "budget": value.BudgetSeries.Samples, "burn": value.BurnSeries.Samples,
	} {
		if len(samples) == 0 || len(samples) > sloOperationalSeriesLimit {
			t.Fatalf("%s history is empty or exceeds its bound", name)
		}
		for index, sample := range samples {
			at := sample.EvaluationAt
			if at.Before(value.Range.RequestedStart) || at.After(value.Range.RequestedEnd) || index > 0 && !samples[index-1].EvaluationAt.Before(at) ||
				sample.BucketStart.Before(value.Range.RequestedStart) || sample.BucketEnd.After(value.Range.RequestedEnd) ||
				at.Before(sample.BucketStart) || at.After(sample.BucketEnd) {
				t.Fatalf("%s selected point lost its requested bounds or ordering", name)
			}
			oracle, exists := canonical[at]
			if !exists {
				from := at.Add(-cadence)
				var err error
				oracle, err = ReadSloOperationalDetail(t.Context(), databaseURL, OperationalDetailInput{Proof: proof, ID: id,
					Now: clock, At: &at, Start: &from, End: &at, BurnWindow: 3600})
				if err != nil || len(oracle.AttainmentSeries.Samples) != 1 {
					t.Fatalf("canonical selected-point evaluation: %v", err)
				}
				canonical[at] = oracle
			}
			var want api.SloOperationalSample
			switch name {
			case "attainment":
				want = oracle.AttainmentSeries.Samples[0]
			case "budget":
				want = retainedWindowSample(at, oracle.Current.Quality, oracle.Current.Performance, value.Objective.DefinitionRevision)
			case "burn":
				burn := oracle.Burn1h
				if value.BurnSeries.BurnWindowSeconds != nil && *value.BurnSeries.BurnWindowSeconds == 21600 {
					burn = oracle.Burn6h
				}
				want = retainedWindowSample(at, burn.Quality, burn.Performance, value.Objective.DefinitionRevision)
			}
			want.BucketStart, want.BucketEnd, want.RetainedExtrema = sample.BucketStart, sample.BucketEnd, sample.RetainedExtrema
			if !reflect.DeepEqual(sample, want) {
				t.Fatalf("%s exact selected point differs from canonical at %s", name, at)
			}
		}
	}
}

func verifyReadinessAdmission(t *testing.T, runtime *Runtime) {
	t.Helper()
	var held []*pgxpool.Conn
	defer func() {
		for _, conn := range held {
			conn.Release()
		}
	}()
	for range 8 {
		conn, err := runtime.Acquire(t.Context(), InteractiveTraffic)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	if err := runtime.CheckReady(t.Context()); err != nil {
		t.Fatalf("schema readiness used saturated interactive admission: %v", err)
	}
	owner, err := runtime.ReadInitialOwnerState(t.Context())
	if err != nil || !owner.Provisioned {
		t.Fatalf("owner readiness used saturated interactive admission: %+v,%v", owner, err)
	}
}

func verifyDerivedBlockFences(t *testing.T, runtime *Runtime, databaseURL, id string, clock func() time.Time) {
	if err := runtime.sampleDerivedMetrics(t.Context(), clock()); err != nil {
		t.Fatalf("derived metrics query: %v", err)
	}
	sample := runtime.metrics.diagnostics.Load()
	if sample == nil || sample.CanonicalBytes <= 0 || sample.DerivedBytes <= 0 || sample.CacheBytes <= 0 || sample.CacheLimitBytes != 2147483648 {
		t.Fatalf("derived metrics did not measure retained relations/cache: %+v", sample)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	pgID, _ := canonicalUUID(id)
	definition, err := q.GetSlo(t.Context(), pgID)
	if err != nil {
		t.Fatal(err)
	}
	params := dbgen.ReadReusableSloBlocksParams{SloID: pgID, SliID: definition.SliID, DefinitionRevision: definition.DefinitionRevision, MeasurementRevision: definition.MeasurementRevision, CalculationVersion: operationalCalculationVersion, Lane: "budget"}
	var start, end, retention pgtype.Timestamptz
	var include bool
	var source int64
	if err := tx.QueryRow(t.Context(), `SELECT range_start,range_end,include_start,source_generation,retention_start,window_seconds,freshness_grace_us FROM public.almanac_slo_display_blocks WHERE slo_id=$1 AND lane='budget' ORDER BY range_end DESC LIMIT 1`, pgID).Scan(&start, &end, &include, &source, &retention, &params.WindowSeconds, &params.FreshnessGraceUs); err != nil {
		t.Fatalf("read persisted budget block: %v", err)
	}
	params.Starts, params.Ends, params.IncludeStarts, params.RetentionStart = []pgtype.Timestamptz{start}, []pgtype.Timestamptz{end}, []bool{include}, retention
	assertBlocks := func(want int) {
		t.Helper()
		rows, err := q.ReadReusableSloBlocks(t.Context(), params)
		if err != nil || len(rows) != want {
			t.Fatalf("reusable block count=%d,want=%d: %v", len(rows), want, err)
		}
	}
	assertBlocks(1)
	// A budget error must roll back both payload replacement and accounting.
	var originalUsage int64
	if err := tx.QueryRow(t.Context(), `SELECT payload_bytes FROM public.almanac_derived_cache_usage`).Scan(&originalUsage); err != nil {
		t.Fatal(err)
	}
	savepoint, err := tx.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := savepoint.Exec(t.Context(), `UPDATE public.almanac_derived_cache_usage SET payload_bytes=2147483648`); err != nil {
		t.Fatal(err)
	}
	_, err = savepoint.Exec(t.Context(), `UPDATE public.almanac_slo_display_blocks SET payload=payload||'{"budget_probe":"x"}'::jsonb WHERE slo_id=$1 AND lane='budget' AND range_start=$2 AND range_end=$3`, pgID, start, end)
	if failure, ok := errors.AsType[*pgconn.PgError](err); !ok || failure.Code != "54000" {
		t.Fatalf("cache budget did not reject mutation: %v", err)
	}
	if err := savepoint.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var restoredUsage int64
	if err := tx.QueryRow(t.Context(), `SELECT payload_bytes FROM public.almanac_derived_cache_usage`).Scan(&restoredUsage); err != nil || restoredUsage != originalUsage {
		t.Fatalf("cache limit failure corrupted accounting: %d/%d %v", restoredUsage, originalUsage, err)
	}
	assertBlocks(1)
	// Changed content with an old digest must be rejected even if its provenance
	// and JSON shape still look plausible.
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_slo_display_blocks SET payload=payload||'{"corruption_probe":true}'::jsonb WHERE slo_id=$1 AND lane='budget' AND range_start=$2 AND range_end=$3`, pgID, start, end); err != nil {
		t.Fatal(err)
	}
	assertBlocks(0)
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_slo_display_blocks SET payload=payload-'corruption_probe' WHERE slo_id=$1 AND lane='budget' AND range_start=$2 AND range_end=$3`, pgID, start, end); err != nil {
		t.Fatal(err)
	}
	assertBlocks(1)
	hour := end.Time.Add(-time.Hour).Truncate(time.Hour)
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at) VALUES($1,$2,$3,$4,$4::timestamptz+interval '1 hour',$5,$5)`, definition.SliID, definition.MeasurementRevision, source+1, hour, clock()); err != nil {
		t.Fatal(err)
	}
	assertBlocks(0)
	jobs, err := q.ClaimDerivedDirtyBatch(t.Context(), dbgen.ClaimDerivedDirtyBatchParams{Now: timestamp(clock()), LeaseUntil: timestamp(clock().Add(time.Minute)), BatchLimit: 1})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim invalidated hour: %v", err)
	}
	job := jobs[0]
	batch := dbgen.RebuildDerivedBatchParams{SliIds: []pgtype.UUID{job.SliID}, Revisions: []int64{job.MeasurementRevision}, Generations: []int64{job.Generation}, Starts: []pgtype.Timestamptz{job.RangeStart}}
	ack := dbgen.AcknowledgeDerivedBatchParams{SliIds: batch.SliIds, Revisions: batch.Revisions, Generations: batch.Generations, Starts: batch.Starts, Epochs: []int64{job.LeaseEpoch}}
	if err := q.RebuildDerivedBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	// Inject the newer arrival between rebuild and acknowledgment. The older
	// epoch cannot erase it; the replacement hour retains block invalidation.
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_derived_dirty_ranges SET generation=generation+1 WHERE sli_id=$1 AND measurement_revision=$2 AND range_start=$3`, job.SliID, job.MeasurementRevision, job.RangeStart); err != nil {
		t.Fatal(err)
	}
	if err := q.AcknowledgeDerivedBatch(t.Context(), ack); err != nil {
		t.Fatal(err)
	}
	var generation int64
	var lease pgtype.Timestamptz
	if err := tx.QueryRow(t.Context(), `SELECT generation,lease_until FROM public.almanac_derived_dirty_ranges WHERE sli_id=$1 AND measurement_revision=$2 AND range_start=$3`, job.SliID, job.MeasurementRevision, job.RangeStart).Scan(&generation, &lease); err != nil || generation != job.Generation+1 || lease.Valid {
		t.Fatalf("old acknowledgment removed or held newer work: generation=%d,lease=%v,%v", generation, lease, err)
	}
	assertBlocks(0)
	ack.Generations[0] = generation
	if err := q.AcknowledgeDerivedBatch(t.Context(), ack); err != nil {
		t.Fatal(err)
	}
	assertBlocks(0)
}

func verifyReadyOverviewReuse(t *testing.T, runtime *Runtime, databaseURL string, proof DatasourceProof, clock func() time.Time) {
	if err := runtime.refreshCurrentProjections(t.Context(), clock); err != nil {
		t.Fatal(err)
	}
	input := OperationalOverviewInput{Runtime: runtime, Proof: proof, Query: "operational", Limit: 1, Now: clock}
	first, err := ReadSloOperationalOverview(t.Context(), databaseURL, input)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("ready overview admission: %v", err)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	defer func() {
		if _, err := conn.Exec(t.Context(), `DELETE FROM public.almanac_slo_overview_snapshots WHERE query_text=$1 AND session_verifier=$2`, input.Query, proof.SessionVerifier[:]); err != nil {
			t.Error(err)
		}
	}()
	var originalExpiry time.Time
	if err := conn.QueryRow(t.Context(), `SELECT expires_at FROM public.almanac_slo_overview_snapshots WHERE id=$1`, first.SnapshotId.String()).Scan(&originalExpiry); err != nil {
		t.Fatal(err)
	}
	admission, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(admission)
	if _, err := admission.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('slo_overview_snapshot_admission',0))`); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		page, err := ReadSloOperationalOverview(t.Context(), databaseURL, input)
		if err != nil || page.SnapshotId != first.SnapshotId || !reflect.DeepEqual(page.Items, first.Items) {
			t.Fatalf("unchanged overview spent another snapshot slot or changed its immutable values: %v", err)
		}
	}
	if err := admission.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	var expiry time.Time
	if err := conn.QueryRow(t.Context(), `SELECT count(*),max(expires_at) FROM public.almanac_slo_overview_snapshots WHERE query_text=$1 AND session_verifier=$2`, input.Query, proof.SessionVerifier[:]).Scan(&count, &expiry); err != nil || count != 1 || !expiry.Equal(originalExpiry) {
		t.Fatalf("reuse renewed expiry or consumed admission: count=%d, expiry=%v, %v", count, expiry, err)
	}
	// A new publication must create a fresh immutable snapshot, retaining the
	// preceding page for existing cursors. No canonical measurement changes.
	id, _ := canonicalUUID(first.Items[0].Slo.Id.String())
	if err := runtime.refreshCurrentProjection(t.Context(), id, clock); err != nil {
		t.Fatal(err)
	}
	changed, err := ReadSloOperationalOverview(t.Context(), databaseURL, input)
	if err != nil || changed.SnapshotId == first.SnapshotId {
		t.Fatalf("new publication reused old page: %v", err)
	}
	var originalState string
	if err := conn.QueryRow(t.Context(), `SELECT state FROM public.almanac_slo_overview_snapshots WHERE id=$1`, first.SnapshotId.String()).Scan(&originalState); err != nil || originalState != "ready" {
		t.Fatalf("refresh removed or mutated the preceding snapshot: %v", err)
	}
	for _, query := range []string{"operational", "operational-a", "operational-h", "operational-w", "-attention", "-watch", "-healthy"} {
		single := input
		single.Query, single.Limit = query, 50
		page, err := ReadSloOperationalOverview(t.Context(), databaseURL, single)
		if err != nil || page.NextCursor != "" {
			t.Fatalf("single-page filtering spent cursor admission: query=%s,%v", query, err)
		}
		var retained bool
		if err := conn.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM public.almanac_slo_overview_snapshots WHERE id=$1)`, page.SnapshotId.String()).Scan(&retained); err != nil || retained {
			t.Fatalf("single-page result retained an unnecessary cursor snapshot: %v", err)
		}
	}
}
