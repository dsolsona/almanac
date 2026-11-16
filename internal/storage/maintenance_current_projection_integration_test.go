//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationMaintenanceCurrentProjection(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	if err := f.runtime.refreshCurrentProjection(t.Context(), h.SloID, f.clock); err != nil {
		t.Fatal(err)
	}
	row, err := dbgen.New(f.conn).GetPublishedCurrentProjection(t.Context(), dbgen.GetPublishedCurrentProjectionParams{SloID: h.SloID, Now: timestamp(f.now), CalculationVersion: operationalCalculationVersion})
	if err != nil || !row.Current || row.ExclusionRevision.Int64 != 1 {
		t.Fatalf("background projection did not publish captured authority: current=%v R=%d err=%v", row.Current, row.ExclusionRevision.Int64, err)
	}
	got, err := publishedCurrentStatus(row)
	if err != nil || got.Burn6h.Performance.Total == nil || *got.Burn6h.Performance.Total != "170000.000000000000" || got.Burn6h.Quality.ExcludedIntervals == nil || *got.Burn6h.Quality.ExcludedIntervals != 10 || got.Burn6h.CalculationProvenance == nil || got.Burn6h.CalculationProvenance.ComputedRevision != "1" || got.Generation.ExclusionRevision != "1" {
		t.Fatalf("background projection disagrees with shared current: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceCurrentProjectionSnapshotFence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"source", "exclusion"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, h := hourFixture(t)
			if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
			locker := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(locker)
			lock, err := locker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(lock)
			if _, err := lock.Exec(t.Context(), `SELECT id FROM almanac_slis WHERE id=$1 FOR UPDATE`, h.SliID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- f.runtime.refreshCurrentProjection(t.Context(), h.SloID, f.clock) }()
			waitForAgentQueryWait(t, f.conn, "%-- name: LockMaintenanceHourSli %")
			if mode == "source" {
				_, err = lock.Exec(t.Context(), `UPDATE almanac_sli_source_generations SET generation=generation+1 WHERE sli_id=$1`, h.SliID)
			} else {
				_, err = lock.Exec(t.Context(), `UPDATE almanac_slo_exclusion_state SET revision=revision+1 WHERE slo_id=$1`, h.SloID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrDerivedPending) {
					t.Fatalf("changed snapshot published current values: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("projection did not finish after source lock release")
			}
			var rows int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_current_projections WHERE slo_id=$1`, h.SloID).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("stale current publication rows=%d err=%v", rows, err)
			}
		})
	}
}

func TestIntegrationMaintenanceCurrentProjectionQueuesPreparation(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	if err := f.runtime.refreshCurrentProjection(t.Context(), h.SloID, f.clock); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("unprepared current was published: %v", err)
	}
	var jobs, projections int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_preparation WHERE kind='scoped_hour' AND state='pending'),(SELECT count(*) FROM almanac_slo_current_projections WHERE slo_id=$1)`, h.SloID).Scan(&jobs, &projections); err != nil || jobs != 1 || projections != 0 {
		t.Fatalf("pending background work lost preparation or published partial values: jobs=%d projections=%d err=%v", jobs, projections, err)
	}
	if turn, err := f.runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock); err != nil || turn.Complete != 1 {
		t.Fatalf("current preparation: %+v %v", turn, err)
	}
	if err := f.runtime.refreshCurrentProjection(t.Context(), h.SloID, f.clock); err != nil {
		t.Fatalf("current did not recover after preparation: %v", err)
	}
}
