//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"
)

func TestIntegrationMaintenanceExtremaSourceWorker(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	for range 10 {
		worked, err := f.runtime.buildSloExtrema(t.Context(), f.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	var payload []byte
	if err := f.conn.QueryRow(t.Context(), `SELECT lanes FROM almanac_slo_extrema WHERE slo_id=$1 AND exclusion_revision=1 AND resolution_seconds=3600 AND range_start=$2`, h.SloID, h.HourStart).Scan(&payload); err != nil {
		t.Fatalf("source worker never published maintenance-aware history: %v", err)
	}
	var lanes extremaLanes
	if err := unmarshalReceiptMetadata(payload, &lanes); err != nil {
		t.Fatal(err)
	}
	session, _, _, base := extremaProducerSession(t, f, h)
	defer rollbackTransaction(session.tx)
	base.At = h.HourStart.Add(time.Hour)
	mark := lanes.Window.Marks[len(lanes.Window.Marks)-1]
	got, err := mark.result(t.Context(), base)
	if err != nil || got.Total == nil || *got.Total != "50000.000000000000" || got.Good == nil || *got.Good != "49500.000000000000" || got.Excluded != 10 || mark.Maintenance == nil || mark.Maintenance.RequestedRevision != 1 {
		t.Fatalf("source artifact differs from exact adjusted window: %+v %v", got, err)
	}
	rollbackTransaction(session.tx)
	if err := f.runtime.refreshCurrentProjection(t.Context(), h.SloID, f.clock); err != nil {
		t.Fatal(err)
	}
	var history string
	if err := f.conn.QueryRow(t.Context(), `SELECT payload->>'budget_history_state' FROM almanac_slo_current_projections WHERE slo_id=$1`, h.SloID).Scan(&history); err != nil || history != "ready" {
		t.Fatalf("closed adjusted hours did not supply list history: %s %v", history, err)
	}
}

func TestIntegrationMaintenanceExtremaSourcePending(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	// Select this parent deterministically without altering its source boundary.
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slo_extrema_cursor SET next_attempt_at=$2 WHERE slo_id<>$1`, h.SloID, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.runtime.buildSloExtrema(t.Context(), f.clock); !worked || !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("missing population was not deferred: %v %v", worked, err)
	}
	var jobs, rows, attempts int
	var leased bool
	var built time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_preparation WHERE kind='scoped_hour' AND state='pending'),(SELECT count(*) FROM almanac_slo_extrema WHERE slo_id=$1),attempts,lease_until IS NOT NULL,built_through FROM almanac_slo_extrema_cursor WHERE slo_id=$1`, h.SloID).Scan(&jobs, &rows, &attempts, &leased, &built); err != nil || jobs != 1 || rows != 0 || attempts != 0 || leased || !built.Equal(h.HourStart) {
		t.Fatalf("pending source lost boundary/ownership: jobs=%d rows=%d attempts=%d leased=%v built=%v err=%v", jobs, rows, attempts, leased, built, err)
	}
	if turn, err := f.runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock); err != nil || turn.Complete != 1 {
		t.Fatalf("source child: %+v %v", turn, err)
	}
	f.now = f.now.Add(time.Second)
	if _, err := f.runtime.buildSloExtrema(t.Context(), f.clock); err != nil {
		t.Fatalf("source parent failed to resume: %v", err)
	}
}

func TestIntegrationMaintenanceExtremaSourceSnapshotFence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"source", "exclusion", "lease"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, h := hourFixture(t)
			if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
			if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slo_extrema_cursor SET next_attempt_at=$2 WHERE slo_id<>$1`, h.SloID, f.now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			calls := 0
			clock := func() time.Time {
				calls++
				if calls == 2 {
					switch mode {
					case "source":
						if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sli_source_generations SET generation=generation+1 WHERE sli_id=$1`, h.SliID); err != nil {
							t.Fatal(err)
						}
					case "exclusion":
						if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("source-phase-change")); err != nil {
							t.Fatal(err)
						}
					case "lease":
						f.now = f.now.Add(31 * time.Second)
					}
				}
				return f.now
			}
			if _, err := f.runtime.buildSloExtrema(t.Context(), clock); !errors.Is(err, ErrDerivedPending) || calls < 2 {
				t.Fatalf("snapshot/lease mutation escaped publication fence: %v calls=%d", err, calls)
			}
			var rows int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_extrema WHERE slo_id=$1`, h.SloID).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("stale source turn committed rows=%d %v", rows, err)
			}
		})
	}
}
