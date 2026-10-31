//go:build integration

package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		for _, table := range []string{"almanac_slo_current_projections", "almanac_slo_history_jobs", "almanac_slo_current_failures", "almanac_slo_derived_generations", "almanac_slo_definitions", "almanac_slos"} {
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

func verifyHistoryAdmissionSnapshotFences(t *testing.T, databaseURL, template string, clock func() time.Time) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global=%v", global), func(t *testing.T) {
			conn := integrationConnect(t, databaseURL)
			defer closeIntegrationConn(conn)
			ids, cleanup := cloneReviewObjectives(t, conn, template, "review-cap", 9)
			defer cleanup()
			var existing int
			if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_history_jobs`).Scan(&existing); err != nil || existing >= 56 {
				t.Fatalf("fixture history queue: %d, %v", existing, err)
			}
			seed := 7
			if global {
				seed = 63 - existing
			}
			admit := func(q *dbgen.Queries, id pgtype.UUID, offset int) (int64, error) {
				at := clock().Add(-time.Duration(offset) * time.Minute)
				return q.AdmitSloHistoryJob(t.Context(), dbgen.AdmitSloHistoryJobParams{SloID: id, DefinitionRevision: 1, MeasurementRevision: 1,
					CalculationVersion: operationalCalculationVersion, RetentionStart: timestamp(clock().Add(-400 * 24 * time.Hour)), Kind: "detail",
					EvaluatedThrough: timestamp(at), RangeStart: timestamp(at.Add(-time.Hour)), RangeEnd: timestamp(at), BurnWindowSeconds: 3600, NextAttemptAt: timestamp(clock())})
			}
			for i := range seed {
				index := 0
				if i >= 7 {
					index = 1 + (i-7)/8
				}
				if changed, err := admit(dbgen.New(conn), ids[index], i+10); err != nil || changed != 1 {
					t.Fatalf("seed history admission: %d, %v", changed, err)
				}
			}
			other := integrationConnect(t, databaseURL)
			defer closeIntegrationConn(other)
			first, err := conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(first)
			second, err := other.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(second)
			// Establish both old snapshots before the first contender commits.
			for _, tx := range []pgx.Tx{first, second} {
				var count int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_history_jobs`).Scan(&count); err != nil {
					t.Fatal(err)
				}
			}
			if err := dbgen.New(first).LockHistoryAdmission(t.Context()); err != nil {
				t.Fatal(err)
			}
			if changed, err := admit(dbgen.New(first), ids[0], 1); err != nil || changed != 1 {
				t.Fatalf("first contender: %d, %v", changed, err)
			}
			if err := first.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			err = dbgen.New(second).LockHistoryAdmission(t.Context())
			if failure, ok := errors.AsType[*pgconn.PgError](err); !ok || failure.Code != "40001" {
				t.Fatalf("stale admission snapshot was not fenced: %v", err)
			}
			if err := second.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			// A fresh transaction must see the full global/per-objective queue.
			id := ids[0]
			if global {
				id = ids[8] // A different objective also cannot exceed global64.
			}
			if changed, err := admit(dbgen.New(other), id, 2); err != nil || changed != 0 {
				t.Fatalf("fresh admission exceeded its cap: %d, %v", changed, err)
			}
		})
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
			// work must receive a turn ahead of the blocked objectives.
			schedulerNow = schedulerNow.Add(time.Minute)
		}
		if err := runtime.refreshCurrentProjections(t.Context(), schedulerClock); err != nil && !errors.Is(err, ErrDerivedSourceFailed) {
			t.Fatal(err)
		}
	}
	projection, err := dbgen.New(conn).GetPublishedCurrentProjection(t.Context(), dbgen.GetPublishedCurrentProjectionParams{SloID: last, Now: timestamp(schedulerNow), CalculationVersion: operationalCalculationVersion})
	if err != nil || !projection.Current {
		t.Fatalf("100 blocked objectives starved the healthy tail: %v", err)
	}
	var deferred int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_current_failures WHERE slo_id=ANY($1::uuid[]) AND attempts=0 AND next_attempt_at>$2`, ids[:100], clock()).Scan(&deferred); err != nil || deferred != 100 {
		t.Fatalf("source waits spent calculation attempts or were not deferred: %d, %v", deferred, err)
	}
	input := OperationalOverviewInput{Runtime: runtime, Proof: proof, Query: "review-", Limit: 50, Now: schedulerClock}
	if _, err := ReadSloOperationalOverview(t.Context(), databaseURL, input); !errors.Is(err, ErrDerivedSourceFailed) {
		t.Fatalf("overview hid exhausted source work as pending: %v", err)
	}
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
	if _, err := ReadSloOperationalOverview(t.Context(), databaseURL, input); !errors.Is(err, ErrDerivedFailed) || errors.Is(err, ErrDerivedSourceFailed) {
		t.Fatalf("overview hid a later exhausted current calculation: %v", err)
	}
	input.Query = "review-healthy"
	if page, err := ReadSloOperationalOverview(t.Context(), databaseURL, input); err != nil || len(page.Items) != 1 {
		t.Fatalf("unrelated healthy filtered rows were unavailable: %v", err)
	}
}
