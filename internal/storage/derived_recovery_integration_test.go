//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
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
	oracle, err := readOperationalDetailForTest(t.Context(), databaseURL, input)
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
	verifyCurrentStatusBoundaryRead(t, databaseURL, runtime, proof, id, clock(), status)
	verifyRetainedRepairBatch(t, databaseURL, proof, id, clock, oracle)
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	input.Runtime = runtime
	partial, err := readOperationalDetailForTest(t.Context(), databaseURL, input)
	if err != nil || partial.HistoryPreparation == nil || partial.HistoryPreparation.ReadySeconds >= partial.HistoryPreparation.TotalSeconds || !reflect.DeepEqual(partial.Current, oracle.Current) {
		t.Fatalf("initial history did not preserve current values and disclose incomplete coverage: %+v, %v", partial.HistoryPreparation, err)
	}
	for attempt := range 1000 {
		worked, err := runtime.buildSloExtrema(t.Context(), clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
		if attempt == 999 {
			t.Fatal("extrema bootstrap did not drain its bounded fixture")
		}
	}
	prepared, err := readOperationalDetailForTest(t.Context(), databaseURL, input)
	if err != nil || prepared.Generation == nil || prepared.HistoryPreparation != nil {
		t.Fatalf("history was not published: %v", err)
	}
	verifyDetachedRetainedSnapshot(t, databaseURL, input, prepared)
	verifyRetainedSelectedValues(t, runtime, databaseURL, proof, id, clock, prepared)
	if !reflect.DeepEqual(prepared.ComplianceSeries, oracle.ComplianceSeries) || !reflect.DeepEqual(prepared.ComplianceSummary, oracle.ComplianceSummary) {
		for index := range min(len(prepared.ComplianceSeries.Days), len(oracle.ComplianceSeries.Days)) {
			if !reflect.DeepEqual(prepared.ComplianceSeries.Days[index], oracle.ComplianceSeries.Days[index]) {
				actual, _ := json.Marshal(prepared.ComplianceSeries.Days[index])
				want, _ := json.Marshal(oracle.ComplianceSeries.Days[index])
				t.Logf("first differing retained/canonical compliance day: %s / %s", actual, want)
				break
			}
		}
		t.Fatal("retained completed-day compliance differs from canonical evaluation")
	}
	t.Run("one evaluation time across minute boundary", func(t *testing.T) {
		crossing := input
		crossing.Period = "90d"
		now, calls := clock(), 0
		crossing.Now = func() time.Time {
			calls++
			if calls > 2 {
				return now.Add(time.Minute)
			}
			return now
		}
		value, err := readOperationalDetailForTest(t.Context(), databaseURL, crossing)
		if err != nil || value.Generation == nil || !value.EffectiveAt.Equal(prepared.EffectiveAt) ||
			!value.Generation.EvaluatedThrough.Equal(value.EffectiveAt) || !value.Range.EffectiveEnd.Equal(value.EffectiveAt) {
			t.Fatalf("minute boundary mixed response evaluation and generation: effective=%s generation=%+v err=%v", value.EffectiveAt, value.Generation, err)
		}
	})
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
	repaired, err := readOperationalDetailForTest(t.Context(), databaseURL, input)
	if err != nil || repaired.Generation == nil || repaired.Generation.GenerationId == prepared.Generation.GenerationId {
		t.Fatalf("corrupt response cache was not replaced directly from retained series: %v", err)
	}
	verifyConcurrentRetainedPublication(t, databaseURL, input, repaired)
	q := dbgen.New(conn)
	// A refreshed display name is not a new measurement/definition revision.
	if _, _, err := UpdateObjectiveMetadata(t.Context(), databaseURL, proof, id, "Renamed objective", "derived-rename", 1, clock, true); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slos SET display_name=$2,version=1,updated_at=$3 WHERE id=$1`, pgID, oracle.Objective.DisplayName, oracle.Objective.UpdatedAt); err != nil {
			t.Error(err)
		}
	}()
	renamed, err := readOperationalDetailForTest(t.Context(), databaseURL, input)
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
	verifyCurrentProjectionConflictBudget(t, runtime, databaseURL, pgID, clock)
	verifyReadyOverviewReuse(t, runtime, databaseURL, proof, clock)
	verifyAuditedRetentionCleanup(t, runtime, databaseURL, id, clock())
}

func verifyCurrentProjectionConflictBudget(t *testing.T, runtime *Runtime, databaseURL string, id pgtype.UUID, clock func() time.Time) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.almanac_test_current_conflict() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION USING ERRCODE=TG_ARGV[0],MESSAGE='fixture current publication failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := conn.Exec(t.Context(), `DROP TRIGGER IF EXISTS almanac_test_current_conflict ON public.almanac_slo_current_projections;
DROP FUNCTION public.almanac_test_current_conflict()`); err != nil {
			t.Error(err)
		}
	}()
	for _, test := range []struct {
		code     string
		calls    int
		attempts int
	}{{"40001", 6, 0}, {"XX000", 1, 1}, {"40001", 6, 1}} {
		statement := `CREATE TRIGGER almanac_test_current_conflict BEFORE INSERT OR UPDATE ON public.almanac_slo_current_projections
FOR EACH ROW EXECUTE FUNCTION public.almanac_test_current_conflict('` + test.code + `')`
		if _, err := conn.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
		for range test.calls {
			err := runtime.refreshCurrentProjection(t.Context(), id, clock)
			if failure, ok := errors.AsType[*pgconn.PgError](err); !ok || failure.Code != test.code {
				t.Fatalf("expected controlled current publication error %s, got %v", test.code, err)
			}
			if errors.Is(err, ErrDerivedPending) != (test.code == "40001") {
				t.Fatalf("current concurrency conflict or genuine failure misclassified: %v", err)
			}
		}
		var attempts int
		if err := conn.QueryRow(t.Context(), `SELECT attempts FROM public.almanac_slo_current_failures WHERE slo_id=$1`, id).Scan(&attempts); err != nil || attempts != test.attempts {
			t.Fatalf("current concurrency conflicts consumed or reset the calculation failure budget: code=%s attempts=%d want=%d err=%v", test.code, attempts, test.attempts, err)
		}
		if _, err := conn.Exec(t.Context(), `DROP TRIGGER almanac_test_current_conflict ON public.almanac_slo_current_projections`); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtime.refreshCurrentProjection(t.Context(), id, clock); err != nil {
		t.Fatalf("current projection did not recover after conflicts ended: %v", err)
	}
	var remaining int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_current_failures WHERE slo_id=$1`, id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("successful current publication retained failure state: %d %v", remaining, err)
	}
}

func verifyDetachedRetainedSnapshot(t *testing.T, databaseURL string, input OperationalDetailInput, oracle api.SloOperationalDetail) {
	t.Helper()
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(t.Context(), databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	id, _ := canonicalUUID(input.ID)
	row, err := q.GetSlo(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	reader := &extremaReader{retainedSeriesReader: retainedSeriesReader{q: q, row: row, source: source, now: input.Now()}}
	bound, err := resolveOperationalBounds(input, time.Duration(row.CadenceSeconds)*time.Second, row.RollingWindowSeconds)
	if err != nil {
		t.Fatal(err)
	}
	base := extremaWindowInput(row, source, input.Now(), input.Now().Add(-365*24*time.Hour))
	if err := reader.prepare(t.Context(), base, bound); err != nil {
		t.Fatal(err)
	}
	input.retainedReader = &reader.retainedSeriesReader
	released := false
	input.releaseSnapshot = func() error {
		if err := finishAgentRead(t.Context(), tx, input.Now, started, user, installation, session); err != nil {
			return err
		}
		closeInitialOwnerConnection(conn)
		if input.Runtime.pools[InteractiveTraffic].Stat().AcquiredConns() != 0 {
			t.Fatal("detached assembly retained a database connection")
		}
		reader.q = nil
		released = true
		return nil
	}
	actual, err := buildSloOperationalDetail(t.Context(), q, row, source, input)
	if err != nil || !released || !reflect.DeepEqual(actual.Current, oracle.Current) || !reflect.DeepEqual(actual.BudgetSeries, oracle.BudgetSeries) ||
		!reflect.DeepEqual(actual.BurnSeries, oracle.BurnSeries) || !reflect.DeepEqual(actual.AttainmentSeries, oracle.AttainmentSeries) ||
		!reflect.DeepEqual(actual.ComplianceSeries, oracle.ComplianceSeries) || !reflect.DeepEqual(actual.ComplianceSummary, oracle.ComplianceSummary) {
		t.Fatalf("detached snapshot lost exact values, graph coverage or daily compliance: %v", err)
	}
}

func verifyConcurrentRetainedPublication(t *testing.T, databaseURL string, input OperationalDetailInput, oracle api.SloOperationalDetail) {
	t.Helper()
	type outcome struct {
		value api.SloOperationalDetail
		err   error
	}
	results := make(chan outcome, 10)
	start := make(chan struct{})
	var pending sync.WaitGroup
	for index := range 10 {
		pending.Go(func() {
			<-start
			end := oracle.EffectiveAt
			from := end.Add(-time.Duration(38+index) * time.Duration(oracle.Objective.CadenceSeconds) * time.Second)
			request := input
			request.Start, request.End, request.Period = &from, &end, ""
			value, err := readOperationalDetailForTest(t.Context(), databaseURL, request)
			results <- outcome{value, err}
		})
	}
	close(start)
	pending.Wait()
	close(results)
	var published []string
	for result := range results {
		if result.err != nil || result.value.Generation == nil || result.value.HistoryPreparation != nil || !reflect.DeepEqual(result.value.Current, oracle.Current) {
			t.Fatalf("concurrent response publication lost a coherent usable result: %v", result.err)
		}
		published = append(published, result.value.Generation.GenerationId.String())
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var accounted, actual int64
	if err := conn.QueryRow(t.Context(), `SELECT payload_bytes,(SELECT coalesce(sum(octet_length(payload::text)),0) FROM public.almanac_slo_history_blocks ) FROM public.almanac_derived_cache_usage WHERE singleton`).Scan(&accounted, &actual); err != nil || accounted != actual {
		t.Fatalf("concurrent response cache accounting is not exact: accounted=%d actual=%d error=%v", accounted, actual, err)
	}
	var invalidMetadata, cached int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE b.payload_bytes IS NULL OR b.payload_bytes<>octet_length(b.payload::text)
OR b.payload_digest<>sha256(convert_to(b.payload::text,'UTF8'))),count(*)
FROM public.almanac_slo_history_blocks b JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id
WHERE g.id=ANY($1::uuid[])`, published).Scan(&invalidMetadata, &cached); err != nil || invalidMetadata != 0 || cached != len(published) {
		t.Fatalf("published history metadata differs from its immutable payload: invalid=%d cached=%d expected=%d error=%v", invalidMetadata, cached, len(published), err)
	}
	verifyPreparedHistoryLookup(t, conn, published[0], input.Now())
	userID, err := dbgen.New(conn).FindAgentSessionUserID(t.Context(), input.Proof.SessionVerifier[:])
	if err != nil {
		t.Fatal(err)
	}
	var readers []pgx.Tx
	for range 2 {
		reader := integrationConnect(t, databaseURL)
		defer closeIntegrationConn(reader)
		tx, err := reader.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackTransaction(tx)
		if _, err := dbgen.New(tx).LockSessionUserRead(t.Context(), userID); err != nil {
			t.Fatal(err)
		}
		readers = append(readers, tx)
	}
	if _, err := conn.Exec(t.Context(), "SET lock_timeout='25ms'"); err != nil {
		t.Fatal(err)
	}
	// Authority mutations must wait for every concurrent read, not only the first.
	for _, reader := range readers {
		_, err := dbgen.New(conn).LockSessionUser(t.Context(), userID)
		pgErr, ok := errors.AsType[*pgconn.PgError](err)
		if !ok || pgErr.Code != "55P03" {
			t.Fatalf("authority mutation bypassed active shared reads: %v", err)
		}
		if err := reader.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dbgen.New(conn).LockSessionUser(t.Context(), userID); err != nil {
		t.Fatalf("authority mutation stayed blocked after reads completed: %v", err)
	}
}

func verifyCurrentStatusBoundaryRead(t *testing.T, databaseURL string, runtime *Runtime, proof DatasourceProof, id string, now time.Time, published api.SloCurrentStatus) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	pgID, _ := canonicalUUID(id)
	var before, after string
	// Read-side fallback leaves the published projection and extrema work unchanged.
	const state = `SELECT jsonb_build_array(
 (SELECT generation_id FROM public.almanac_slo_current_projections WHERE slo_id=$1),
 (SELECT to_jsonb(r) FROM public.almanac_slo_extrema_cursor r WHERE slo_id=$1))::text`
	if err := conn.QueryRow(t.Context(), state, pgID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	access, err := AuthenticateSession(t.Context(), databaseURL, proof.SessionVerifier, now)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, access.Login)
	if err != nil || !identity.Found {
		t.Fatalf("boundary fixture identity unavailable: %v", err)
	}
	for index, advance := range []time.Duration{time.Duration(published.Slo.CadenceSeconds) * time.Second, 2 * time.Hour} {
		at := now.Add(advance)
		clock := func() time.Time { return at }
		// The long clock-only gap exceeds idle expiry; use a normal fresh session.
		boundaryProof := proof
		boundaryProof.SessionVerifier[31] ^= byte(index + 1)
		if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{UserID: identity.UserID,
			ExpectedCredentialEpoch: identity.CredentialEpoch, ExpectedCredentialRevision: identity.CredentialRevision,
			Verifier: boundaryProof.SessionVerifier, Now: at, RequestID: "boundary-session-" + advance.String()}); err != nil {
			t.Fatal(err)
		}
		oracle, err := readOperationalDetailForTest(t.Context(), databaseURL, OperationalDetailInput{Proof: boundaryProof, ID: id, Now: clock, BurnWindow: 3600})
		if err != nil {
			t.Fatal(err)
		}
		got, err := ReadCurrentSloStatus(t.Context(), databaseURL, CurrentSloInput{Runtime: runtime, Proof: boundaryProof, ID: id, Now: clock})
		if err != nil || !reflect.DeepEqual(got.Current, oracle.Current) || !reflect.DeepEqual(got.Burn1h, oracle.Burn1h) || !reflect.DeepEqual(got.Burn6h, oracle.Burn6h) || !reflect.DeepEqual(got.Exhaustion, oracle.Exhaustion) || string(got.Urgency) != string(oracle.Urgency) {
			t.Fatalf("current boundary read after %s differs from canonical values: %v", advance, err)
		}
		if got.Generation.GenerationId == published.Generation.GenerationId || !got.Generation.EvaluatedThrough.Equal(oracle.EffectiveAt) || !got.Generation.PublishedAt.Equal(at) || got.Generation.SourceGeneration != published.Generation.SourceGeneration {
			t.Fatalf("current boundary read reused invalidated provenance after %s: %+v", advance, got.Generation)
		}
		page, err := readOperationalOverviewForTest(t.Context(), databaseURL, OperationalOverviewInput{Runtime: runtime, Proof: boundaryProof, Query: published.Slo.Name, Limit: 20, Now: clock})
		if err != nil {
			t.Fatalf("last-known list read after %s: %v", advance, err)
		}
		found := false
		for _, item := range page.Items {
			if item.Slo.Id.String() != id {
				continue
			}
			found = true
			if item.CurrentState == nil || *item.CurrentState != "stale" || item.Generation == nil || item.Generation.GenerationId != published.Generation.GenerationId ||
				!reflect.DeepEqual(item.Current, published.Current) || !reflect.DeepEqual(item.Burn1h, published.Burn1h) || item.Urgency == "healthy" || item.Generation.CalculationLagSeconds <= 0 {
				t.Fatalf("last-known list values lost provenance, changed values or appeared fresh after %s", advance)
			}
		}
		if !found {
			t.Fatal("last-known list omitted its objective")
		}
		if index == 0 {
			q := dbgen.New(conn)
			for range 5 {
				if err := q.RecordCurrentPreparationFailure(t.Context(), dbgen.RecordCurrentPreparationFailureParams{SloID: pgID,
					DefinitionRevision: 1, MeasurementRevision: 1, CalculationVersion: operationalCalculationVersion, Column5: timestamp(at)}); err != nil {
					t.Fatal(err)
				}
			}
			failed, readErr := readOperationalOverviewForTest(t.Context(), databaseURL, OperationalOverviewInput{Runtime: runtime, Proof: boundaryProof, Query: published.Slo.Name, Limit: 20, Now: clock})
			if err := q.ClearCurrentPreparationFailure(t.Context(), pgID); err != nil {
				t.Fatal(err)
			}
			if readErr != nil || len(failed.Items) != 1 || failed.Items[0].CurrentState == nil || *failed.Items[0].CurrentState != "failed" ||
				failed.Items[0].Generation == nil || failed.Items[0].Generation.GenerationId != published.Generation.GenerationId || !reflect.DeepEqual(failed.Items[0].Current, published.Current) {
				t.Fatalf("failed current update erased or silently refreshed last-known values: %v", readErr)
			}
		}
		if _, err := RevokeSession(t.Context(), databaseURL, boundaryProof.SessionVerifier, at, "boundary-session-cleanup"); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.QueryRow(t.Context(), state, pgID).Scan(&after); err != nil || before != after {
		t.Fatalf("current boundary read mutated maintenance state: before=%s after=%s, %v", before, after, err)
	}
}

func verifyRetainedSelectedValues(t *testing.T, runtime *Runtime, databaseURL string, proof DatasourceProof, id string, clock func() time.Time, value api.SloOperationalDetail) {
	t.Helper()
	ctx := runtime.Bind(t.Context())
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
				oracle, err = readOperationalDetailForTest(ctx, databaseURL, OperationalDetailInput{Proof: proof, ID: id,
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

func verifyReadyOverviewReuse(t *testing.T, runtime *Runtime, databaseURL string, proof DatasourceProof, clock func() time.Time) {
	if err := runtime.refreshCurrentProjections(t.Context(), clock); err != nil {
		t.Fatal(err)
	}
	input := OperationalOverviewInput{Runtime: runtime, Proof: proof, Query: "operational", Limit: 1, Now: clock}
	first, err := readOperationalOverviewForTest(t.Context(), databaseURL, input)
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
	var referenceRows, copiedRows int
	var referenceBytes, calculationBytes int64
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE i.projection_digest IS NOT NULL),
        count(*) FILTER (WHERE i.payload ? 'current'),sum(i.payload_bytes),sum(a.payload_bytes)
        FROM public.almanac_slo_overview_snapshot_items i
        JOIN public.almanac_slo_projection_payloads a ON a.digest=i.projection_digest WHERE i.snapshot_id=$1`,
		first.SnapshotId.String()).Scan(&referenceRows, &copiedRows, &referenceBytes, &calculationBytes); err != nil || referenceRows != 3 || copiedRows != 0 || referenceBytes >= calculationBytes {
		t.Fatalf("cursor copied calculations instead of pinning bounded references: rows=%d,copied=%d,bytes=%d/%d,%v", referenceRows, copiedRows, referenceBytes, calculationBytes, err)
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
		page, err := readOperationalOverviewForTest(t.Context(), databaseURL, input)
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
	changed, err := readOperationalOverviewForTest(t.Context(), databaseURL, input)
	if err != nil || changed.SnapshotId == first.SnapshotId {
		t.Fatalf("new publication reused old page: %v", err)
	}
	var originalState string
	if err := conn.QueryRow(t.Context(), `SELECT state FROM public.almanac_slo_overview_snapshots WHERE id=$1`, first.SnapshotId.String()).Scan(&originalState); err != nil || originalState != "ready" {
		t.Fatalf("refresh removed or mutated the preceding snapshot: %v", err)
	}
	oldSecond := input
	oldSecond.Cursor = first.NextCursor
	oldPage, err := readOperationalOverviewForTest(t.Context(), databaseURL, oldSecond)
	if err != nil || oldPage.PreviousCursor == "" {
		t.Fatalf("original reference cursor was lost after publication: %v", err)
	}
	oldFirst := input
	oldFirst.Cursor = oldPage.PreviousCursor
	pinnedFirst, err := readOperationalOverviewForTest(t.Context(), databaseURL, oldFirst)
	if err != nil || !reflect.DeepEqual(pinnedFirst.Items, first.Items) || pinnedFirst.GroupCounts != first.GroupCounts {
		t.Fatalf("new publication mutated an archived projection pinned by the old cursor: %v", err)
	}
	for _, query := range []string{"operational", "operational-a", "operational-h", "operational-w", "-attention", "-watch", "-healthy"} {
		single := input
		single.Query, single.Limit = query, 50
		page, err := readOperationalOverviewForTest(t.Context(), databaseURL, single)
		if err != nil || page.NextCursor != "" {
			t.Fatalf("single-page filtering spent cursor admission: query=%s,%v", query, err)
		}
		var retained bool
		if err := conn.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM public.almanac_slo_overview_snapshots WHERE id=$1)`, page.SnapshotId.String()).Scan(&retained); err != nil || retained {
			t.Fatalf("single-page result retained an unnecessary cursor snapshot: %v", err)
		}
	}
}
