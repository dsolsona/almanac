//go:build integration

package storage

import (
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceBudgetRecovery(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"still_pending", "compatibility_completed", "legacy_cache"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			f.drain(t, "budget source")
			for range 10 {
				worked, err := f.runtime.buildSloExtrema(t.Context(), f.clock)
				if err != nil {
					t.Fatal(err)
				}
				if !worked {
					break
				}
			}
			setLegacyMaintenanceAnalysis(t, f.conn, true)
			if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("budget-future")); err != nil {
				t.Fatal(err)
			}
			setLegacyMaintenanceAnalysis(t, f.conn, false)
			build := compilerCapture(t, f, nil)
			compilerFinish(t, f, build.ID, 10)
			f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
			refresh := func() api.SloOperationalOverviewItem {
				t.Helper()
				if err := f.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(f.ids[0]), f.clock); err != nil {
					t.Fatal(err)
				}
				var payload []byte
				if err := f.conn.QueryRow(t.Context(), `SELECT payload FROM almanac_slo_current_projections WHERE slo_id=$1`, f.ids[0]).Scan(&payload); err != nil {
					t.Fatal(err)
				}
				var item api.SloOperationalOverviewItem
				if err := json.Unmarshal(payload, &item); err != nil {
					t.Fatal(err)
				}
				return item
			}
			before := refresh()
			if before.BudgetHistoryState == nil || *before.BudgetHistoryState != "preparing" {
				t.Fatalf("unprepared compatibility history: %v", before.BudgetHistoryState)
			}
			var published time.Time
			if err := f.conn.QueryRow(t.Context(), `SELECT max(published_at) FROM almanac_slo_extrema WHERE slo_id=$1`, f.ids[0]).Scan(&published); err != nil {
				t.Fatal(err)
			}
			want := "preparing"
			if phase != "still_pending" {
				if phase == "legacy_cache" {
					// The previous cache format never recorded sample completeness.
					if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slo_current_projections SET payload=payload#-'{_budget_cache,closed_ready}' WHERE slo_id=$1`, f.ids[0]); err != nil {
						t.Fatal(err)
					}
				}
				compilerTx(t, f, func(tx pgx.Tx) error {
					p, err := advanceMaintenanceCompatibility(t.Context(), tx, build.ID, 10)
					if err == nil && p.State != "complete" {
						t.Fatalf("compatibility did not finish: %+v", p)
					}
					return err
				})
				want = "ready"
			}
			after := refresh()
			if after.BudgetHistoryState == nil {
				t.Fatal("second refresh omitted history state")
			}
			if string(*after.BudgetHistoryState) != want {
				t.Fatalf("second refresh state=%s, want %s", *after.BudgetHistoryState, want)
			}
			var unchanged time.Time
			if err := f.conn.QueryRow(t.Context(), `SELECT max(published_at) FROM almanac_slo_extrema WHERE slo_id=$1`, f.ids[0]).Scan(&unchanged); err != nil || !unchanged.Equal(published) {
				t.Fatalf("fixture republished retained rows: %v", err)
			}
			if phase != "still_pending" {
				// Future maintenance cannot change the fixture's 990/1000 per minute.
				// Recovery must replace the closed preparation gaps, not just its label.
				verifyFixtureSparkline(t, f.selfHealFixture, after)
			}
		})
	}
}

func TestIntegrationMaintenanceBudgetRecoveryTerminalFirstWindow(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "budget-terminal", true)
	valid := s.at.Add(-8 * 24 * time.Hour)
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_measurements SET effective_from=$1 WHERE sli_id=$2`, valid, s.sliID); err != nil {
		t.Fatal(err)
	}
	id := s.createSLOWithHistory(t, "budget-terminal", time.Hour, valid)
	s.drain(t, "terminal budget current source")
	q := dbgen.New(s.conn)
	start := s.at.Add(-7 * 24 * time.Hour)
	if err := q.MarkDerivedHoursDirty(t.Context(), dbgen.MarkDerivedHoursDirtyParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1, RangeStart: timestamp(start.Add(-time.Hour)), RangeEnd: timestamp(start), Now: timestamp(s.now)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_derived_dirty_ranges SET attempts=5,lease_until=NULL WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetSlo(t.Context(), mustPGUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := buildSloOperationalDetail(t.Context(), q, row, source, OperationalDetailInput{currentOnly: true, Now: s.clock, BurnWindow: 3600})
	if err != nil {
		t.Fatal(err)
	}
	item := api.SloOperationalOverviewItem{Current: detail.Current}
	if _, err := attachExtremaBudget(t.Context(), q, &item, row, source, s.now, nil); err != nil {
		t.Fatal(err)
	}
	if item.BudgetHistoryState == nil {
		t.Fatal("exhausted first-window source omitted history state")
	}
	if *item.BudgetHistoryState != "unavailable" {
		t.Fatalf("exhausted first-window source reported %s instead of unavailable", *item.BudgetHistoryState)
	}
}
