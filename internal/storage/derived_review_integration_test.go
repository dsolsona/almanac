//go:build integration

package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func cloneReviewObjectives(t *testing.T, conn *pgx.Conn, template, prefix string, count int) ([]pgtype.UUID, func()) {
	t.Helper()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	ids := make([]pgtype.UUID, count)
	for i := range count {
		ids[i] = pgtype.UUID{Bytes: [16]byte{0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0x4e, 0xee, 0x8e, 0xee, 0, 0, 0, 0, 0, byte(i + 1)}, Valid: true}
		if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_slos(id,name,display_name,definition_revision,created_at,updated_at)
SELECT $1,$2,$2,definition_revision,created_at,updated_at FROM public.almanac_slos WHERE id=$3`, ids[i], fmt.Sprintf("%s-%03d", prefix, i), template); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_slo_definitions
(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at)
SELECT $1,d.revision,d.service_id,d.sli_id,d.measurement_revision,d.target,d.rolling_window_seconds,d.cadence_seconds,d.activated_at,d.evaluation_valid_from,d.historical_requested_by,d.historical_requested_at
FROM public.almanac_slo_definitions d JOIN public.almanac_slos s ON s.id=d.slo_id AND s.definition_revision=d.revision WHERE s.id=$2`, ids[i], template); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return ids, func() {
		tx, err := conn.Begin(t.Context())
		if err != nil {
			t.Error(err)
			return
		}
		defer rollbackTransaction(tx)
		if _, err := tx.Exec(t.Context(), `DELETE FROM public.almanac_slo_overview_snapshots s WHERE EXISTS (
            SELECT 1 FROM public.almanac_slo_overview_snapshot_items i WHERE i.snapshot_id=s.id
              AND (i.payload#>>'{slo,id}')::uuid=ANY($1::uuid[]))`, ids); err != nil {
			t.Error(err)
			return
		}
		if _, err := tx.Exec(t.Context(), `DELETE FROM public.almanac_slo_projection_payloads WHERE (payload#>>'{slo,id}')::uuid=ANY($1::uuid[])`, ids); err != nil {
			t.Error(err)
			return
		}
		for _, table := range []string{"almanac_slo_extrema", "almanac_slo_extrema_cursor", "almanac_slo_current_projections", "almanac_slo_current_failures", "almanac_slo_derived_generations", "almanac_slo_definitions", "almanac_slos"} {
			key := "slo_id"
			if table == "almanac_slos" {
				key = "id"
			}
			if _, err := tx.Exec(t.Context(), "DELETE FROM public."+table+" WHERE "+key+"=ANY($1::uuid[])", ids); err != nil {
				t.Error(err)
				return
			}
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Error(err)
		}
	}
}

func verifyBlockedCurrentFairnessAndFailureStates(t *testing.T, databaseURL string, proof DatasourceProof, blocked, healthy string, clock func() time.Time) {
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	ids, cleanup := cloneReviewObjectives(t, conn, blocked, "review-blocked", 101)
	defer cleanup()
	// The healthy objective sorts after all 100 unpublished blocked objectives.
	last := ids[100]
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slo_definitions d
SET sli_id=h.sli_id,measurement_revision=h.measurement_revision,cadence_seconds=h.cadence_seconds,
    rolling_window_seconds=h.rolling_window_seconds,evaluation_valid_from=h.evaluation_valid_from,activated_at=h.activated_at
FROM public.almanac_slo_definitions h JOIN public.almanac_slos s ON s.id=h.slo_id AND s.definition_revision=h.revision
WHERE d.slo_id=$1 AND s.id=$2`, last, healthy); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slos SET name='review-healthy-tail',display_name='review-healthy-tail' WHERE id=$1`, last); err != nil {
		t.Fatal(err)
	}
	definition, err := dbgen.New(conn).GetSlo(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	hour := clock().Add(-time.Hour).Truncate(time.Hour)
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,attempts,next_attempt_at,updated_at)
VALUES($1,$2,1,$3,$3::timestamptz+interval '1 hour',5,$4,$4)`, definition.SliID, definition.MeasurementRevision, hour, clock()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := conn.Exec(t.Context(), `DELETE FROM public.almanac_derived_dirty_ranges WHERE sli_id=$1 AND measurement_revision=$2 AND range_start=$3`, definition.SliID, definition.MeasurementRevision, hour); err != nil {
			t.Error(err)
		}
	}()
	runtime, err := OpenRuntime(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	schedulerNow := clock()
	schedulerClock := func() time.Time { return schedulerNow }
	for batch := range 2 {
		if batch == 1 {
			// Even when the deferral expires before the next batch, never-tried
			// work must receive a turn. Keep evaluation time fixed so the healthy
			// fixture does not acquire an uncollected future interval.
			if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_failures SET next_attempt_at=$2 WHERE slo_id=ANY($1::uuid[])`, ids[:100], schedulerNow); err != nil {
				t.Fatal(err)
			}
		}
		if err := runtime.refreshCurrentProjections(t.Context(), schedulerClock); err != nil && !errors.Is(err, ErrDerivedSourceFailed) {
			t.Fatal(err)
		}
		if batch == 0 {
			var paused int
			if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_current_failures WHERE slo_id=ANY($1::uuid[]) AND attempts=0 AND next_attempt_at>$2`, ids[:100], clock()).Scan(&paused); err != nil || paused == 0 || paused > 100 {
				t.Fatalf("source waits were not deferred within the first bounded batch: %d, %v", paused, err)
			}
		}
	}
	projection, err := dbgen.New(conn).GetPublishedCurrentProjection(t.Context(), dbgen.GetPublishedCurrentProjectionParams{SloID: last, Now: timestamp(schedulerNow), CalculationVersion: operationalCalculationVersion})
	if err != nil || !projection.Current {
		t.Fatalf("100 blocked objectives starved the healthy tail: %v", err)
	}
	var deferred int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_current_failures WHERE slo_id=ANY($1::uuid[]) AND attempts=0 AND next_attempt_at>=$2`, ids[:100], clock()).Scan(&deferred); err != nil || deferred != 100 {
		t.Fatalf("source waits spent calculation attempts or were not deferred: %d, %v", deferred, err)
	}
	input := OperationalOverviewInput{Runtime: runtime, Proof: proof, Query: "review-", Limit: 50, Now: schedulerClock}
	checkIsolatedFailures := func(currentFailed bool) {
		t.Helper()
		pageInput := input
		sourceFailures, calculationFailures, ready := 0, 0, 0
		for pageIndex := range 3 {
			page, err := readOperationalOverviewForTest(t.Context(), databaseURL, pageInput)
			if err != nil || page.GroupCounts.Watch != 100 || page.GroupCounts.Healthy != 1 {
				t.Fatalf("unavailable rows blocked the list or changed pinned counts: %+v, %v", page.GroupCounts, err)
			}
			for _, item := range page.Items {
				if item.Urgency == "healthy" {
					ready++
					continue
				}
				if item.Current.Quality.State != "unavailable" || item.Current.Quality.Reason == nil || item.Current.Performance.State != "unknown" ||
					item.Current.Performance.Good != nil || item.Current.Performance.RemainingFraction != nil || item.Generation != nil {
					t.Fatal("unavailable projection retained current success or numerical values")
				}
				switch *item.Current.Quality.Reason {
				case "source_aggregate_failed":
					sourceFailures++
				case "current_calculation_failed":
					calculationFailures++
				default:
					t.Fatalf("exhausted row was hidden as pending: %s", *item.Current.Quality.Reason)
				}
			}
			if page.NextCursor == "" {
				break
			}
			if pageIndex == 2 {
				t.Fatal("bounded fixture pagination did not terminate")
			}
			pageInput.Cursor = page.NextCursor
		}
		wantSource, wantCalculation := 100, 0
		if currentFailed {
			wantSource, wantCalculation = 99, 1
		}
		if sourceFailures != wantSource || calculationFailures != wantCalculation || ready != 1 {
			t.Fatalf("row isolation lost failure identity or the healthy tail: source=%d,current=%d,ready=%d", sourceFailures, calculationFailures, ready)
		}
	}
	checkIsolatedFailures(false)
	q := dbgen.New(conn)
	params := dbgen.RecordCurrentPreparationFailureParams{SloID: ids[99], DefinitionRevision: 1, MeasurementRevision: 1, CalculationVersion: operationalCalculationVersion, Column5: timestamp(clock())}
	if err := q.RecordCurrentPreparationFailure(t.Context(), params); err != nil {
		t.Fatal(err)
	}
	if err := q.DeferCurrentPreparation(t.Context(), dbgen.DeferCurrentPreparationParams{SloID: params.SloID, DefinitionRevision: 1, MeasurementRevision: 1, CalculationVersion: operationalCalculationVersion, Column5: timestamp(clock())}); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := conn.QueryRow(t.Context(), `SELECT attempts FROM public.almanac_slo_current_failures WHERE slo_id=$1`, params.SloID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("source deferral reset the existing failure budget: %d, %v", attempts, err)
	}
	for range 4 {
		if err := q.RecordCurrentPreparationFailure(t.Context(), params); err != nil {
			t.Fatal(err)
		}
	}
	checkIsolatedFailures(true)
	input.Query = "review-healthy"
	if page, err := readOperationalOverviewForTest(t.Context(), databaseURL, input); err != nil || len(page.Items) != 1 {
		t.Fatalf("unrelated healthy filtered rows were unavailable: %v", err)
	}
}
