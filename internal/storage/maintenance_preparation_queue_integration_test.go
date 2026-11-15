//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func preparationScope(f maintenanceFixture) maintenancePreparationScope {
	return maintenancePreparationScope{SloID: mustPGUUID(f.ids[0]), SliID: mustPGUUID(f.sliID), DefinitionRevision: 1, MeasurementRevision: 1}
}
func preparationEnqueue(t *testing.T, f maintenanceFixture, requests []maintenanceEvaluationPreparation, priority maintenancePreparationPriority) {
	t.Helper()
	compilerTx(t, f, func(tx pgx.Tx) error {
		p, err := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), requests, maintenancePreparationLimits{MaxRequests: 20, MaxSourceHours: 20}, f.now)
		if err != nil {
			return err
		}
		return enqueueMaintenancePreparation(t.Context(), tx, p, priority, f.now)
	})
}
func TestIntegrationMaintenancePreparationDedupAndRollback(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	m := f.input("queue-one")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	requests := []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 1}, {Kind: "mask", RequestedRevision: 1}, {Kind: "compatibility", RequestedRevision: 1}}
	preparationEnqueue(t, f, requests, maintenancePreparationHistorical)
	preparationEnqueue(t, f, requests, maintenancePreparationLive)
	var count, live int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE priority='live') FROM almanac_maintenance_preparation`).Scan(&count, &live); err != nil {
		t.Fatal(err)
	}
	if count != 2 || live != 2 {
		t.Fatalf("global dedup/promotion %d/%d", count, live)
	}
	m = f.input("queue-two")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p, err := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 2}}, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err = enqueueMaintenancePreparation(t.Context(), tx, p, maintenancePreparationLive, f.now); err != nil {
		t.Fatal(err)
	}
	rollbackTransaction(tx)
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_builds WHERE target_revision=2`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled back pin count %d: %v", count, err)
	}
}
func TestIntegrationMaintenancePreparationSourceBackoff(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	// One healthy hour, one existing exhausted/leased retry, and one missing aggregate.
	retry := h.HourStart.Add(time.Hour)
	missing := retry.Add(time.Hour)
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_derived_dirty_ranges(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at,attempts,lease_epoch,lease_until) SELECT sli_id,measurement_revision,generation,$2,$2::timestamptz+interval '1 hour',$3,$4,5,17,$5 FROM almanac_sli_source_generations WHERE sli_id=$1`, f.sliID, retry, f.now.Add(time.Hour), f.now, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, missing); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := f.conn.QueryRow(t.Context(), `SELECT row_to_json(d)::text FROM almanac_derived_dirty_ranges d WHERE sli_id=$1 AND range_start=$2`, f.sliID, retry).Scan(&before); err != nil {
		t.Fatal(err)
	}
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "source_window", Start: h.HourStart, End: missing.Add(time.Hour)}}, maintenancePreparationLive)
	var after string
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT row_to_json(d)::text FROM almanac_derived_dirty_ranges d WHERE sli_id=$1 AND range_start=$2`, f.sliID, retry).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("source enqueue modified existing backoff/lease/exhaustion")
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_derived_dirty_ranges WHERE sli_id=$1 AND range_start >= $2 AND range_start < $3`, f.sliID, h.HourStart, missing.Add(time.Hour)).Scan(&count); err != nil || count != 2 {
		t.Fatalf("healthy hour dirtied or missing hour lost: %d %v", count, err)
	}
	var generation, current int64
	if err := f.conn.QueryRow(t.Context(), `SELECT d.generation,g.generation FROM almanac_derived_dirty_ranges d JOIN almanac_sli_source_generations g USING(sli_id,measurement_revision) WHERE d.sli_id=$1 AND d.range_start=$2`, f.sliID, missing).Scan(&generation, &current); err != nil || generation != current {
		t.Fatalf("dirty target not current: %d/%d %v", generation, current, err)
	}
}
func TestIntegrationMaintenancePreparationClaimFence(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 0}}, maintenancePreparationLive)
	var first, second dbgen.AlmanacMaintenancePreparation
	claim := func(tick, now time.Time) []dbgen.AlmanacMaintenancePreparation {
		var rows []dbgen.AlmanacMaintenancePreparation
		compilerTx(t, f, func(tx pgx.Tx) error {
			var err error
			rows, err = claimMaintenancePreparation(t.Context(), tx, maintenancePreparationLive, tick, time.Minute, 1, func() time.Time { return now })
			return err
		})
		return rows
	}
	rows := claim(f.now, f.now)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	first = rows[0]
	if rows = claim(f.now, f.now.Add(2*time.Minute)); len(rows) != 0 {
		t.Fatal("same tick reclaimed job")
	}
	rows = claim(f.now.Add(time.Second), f.now.Add(2*time.Minute))
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	second = rows[0]
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	err = finishMaintenancePreparation(t.Context(), tx, first, "complete", "", 0, 5, func() time.Time { return f.now.Add(2 * time.Minute) })
	if !errors.Is(err, ErrMaintenancePreparationStale) {
		t.Fatalf("stale lease ack: %v", err)
	}
	rollbackTransaction(tx)
	compilerTx(t, f, func(tx pgx.Tx) error {
		return finishMaintenancePreparation(t.Context(), tx, second, "pending", "dependency_pending", time.Second, 5, func() time.Time { return f.now.Add(2 * time.Minute) })
	})
	var attempts int
	if err := f.conn.QueryRow(t.Context(), `SELECT attempts FROM almanac_maintenance_preparation WHERE id=$1`, second.ID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("pending charged failure %d %v", attempts, err)
	}
}
