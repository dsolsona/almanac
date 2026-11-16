//go:build integration

package storage

import (
	"math/big"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationMaintenanceWorkerClockPrecision(t *testing.T) {
	t.Parallel()
	for _, maintained := range []bool{false, true} {
		name := "revision_zero"
		if maintained {
			name = "nonzero_revision"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			f.drain(t, "precision canonical source")
			first := f.historyStart.Truncate(time.Hour)
			revision := int64(0)
			expectedGood, expectedTotal, expectedObserved := int64(59400), int64(60000), int64(60)
			if maintained {
				m := alertMaintenanceFixture(f)
				in := m.input("precision-mask")
				in.Start = first.Add(10 * time.Minute)
				in.End = first.Add(20 * time.Minute)
				if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, in); err != nil {
					t.Fatal(err)
				}
				b := compilerCapture(t, m, nil)
				compilerFinish(t, m, b.ID, 64)
				var source int64
				if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, first).Scan(&source); err != nil {
					t.Fatal(err)
				}
				h := maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SloID: mustPGUUID(f.sloID), SliID: mustPGUUID(f.sliID), BuildID: b.ID, DefinitionRevision: 1, MeasurementRevision: 1, HourStart: first, SourceGeneration: source}, RetentionStart: f.historyStart, Deadline: f.now.Add(time.Minute)}
				if _, err := hourPrepare(t, m, h); err != nil {
					t.Fatal(err)
				}
				revision = 1
				expectedGood = 49500
				expectedTotal = 50000
				expectedObserved = 50
			}
			f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
			if _, err := dbgen.New(f.conn).EnsureSloExtremaCursors(t.Context(), dbgen.EnsureSloExtremaCursorsParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(f.now)}); err != nil {
				t.Fatal(err)
			}
			clock := func() time.Time { return f.now.Add(123 * time.Nanosecond) }
			worked, err := f.runtime.buildSloExtrema(t.Context(), clock)
			if err != nil || !worked {
				t.Fatalf("admitted normal worker rejected submicrosecond server clock: worked=%v err=%v", worked, err)
			}
			var raw []byte
			var gotRevision int64
			if err = f.conn.QueryRow(t.Context(), `SELECT lanes,exclusion_revision FROM almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, f.sloID, first).Scan(&raw, &gotRevision); err != nil {
				t.Fatal(err)
			}
			var lanes extremaLanes
			if err = unmarshalReceiptMetadata(raw, &lanes); err != nil {
				t.Fatal(err)
			}
			if gotRevision != revision || lanes.Window == nil {
				t.Fatalf("missing actual requested retained lane: revision=%d want=%d", gotRevision, revision)
			}
			found := false
			for _, mark := range lanes.Window.Marks {
				if !mark.At.Equal(first.Add(time.Hour)) {
					continue
				}
				found = true
				good, gok := new(big.Rat).SetString(mark.Good)
				total, tok := new(big.Rat).SetString(mark.Total)
				if !gok || !tok || good.Cmp(big.NewRat(expectedGood, 1)) != 0 || total.Cmp(big.NewRat(expectedTotal, 1)) != 0 || mark.Observed != expectedObserved {
					t.Fatalf("published retained endpoint is not exact: good=%s total=%s observed=%d", mark.Good, mark.Total, mark.Observed)
				}
			}
			if !found {
				t.Fatal("worker did not publish last endpoint numeric mark")
			}
			var through time.Time
			var attempts int32
			if err = f.conn.QueryRow(t.Context(), `SELECT built_through,attempts FROM almanac_slo_extrema_cursor WHERE slo_id=$1`, f.sloID).Scan(&through, &attempts); err != nil {
				t.Fatal(err)
			}
			if !through.After(first) || attempts != 0 {
				t.Fatalf("worker did not advance durable cursor: through=%v attempts=%d", through, attempts)
			}
			if err = f.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(f.sloID), clock); err != nil {
				t.Fatal(err)
			}
			var payload []byte
			var closedReady bool
			if err = f.conn.QueryRow(t.Context(), `SELECT payload,coalesce((payload#>>'{_budget_cache,closed_ready}')::boolean,false) FROM almanac_slo_current_projections WHERE slo_id=$1`, f.sloID).Scan(&payload, &closedReady); err != nil {
				t.Fatal(err)
			}
			var item api.SloOperationalOverviewItem
			if err = unmarshalReceiptMetadata(payload, &item); err != nil {
				t.Fatal(err)
			}
			if item.BudgetHistoryState == nil || *item.BudgetHistoryState != "ready" || !closedReady || len(item.BudgetSparkline) == 0 {
				t.Fatalf("ready retained rows became unavailable under real server precision: state=%v closedReady=%v points=%d", item.BudgetHistoryState, closedReady, len(item.BudgetSparkline))
			}
			numeric := 0
			for _, sample := range item.BudgetSparkline {
				if sample.Good == nil || sample.Total == nil {
					continue
				}
				numeric++
				observed := int64(0)
				for at := f.historyStart; at.Before(f.at); at = at.Add(time.Minute) {
					if at.Before(sample.EvaluationAt.Add(-time.Hour)) || !at.Before(sample.EvaluationAt) {
						continue
					}
					if maintained && !at.Before(first.Add(10*time.Minute)) && at.Before(first.Add(20*time.Minute)) {
						continue
					}
					observed++
				}
				good, gok := new(big.Rat).SetString(*sample.Good)
				total, tok := new(big.Rat).SetString(*sample.Total)
				if !gok || !tok || good.Cmp(big.NewRat(990*observed, 1)) != 0 || total.Cmp(big.NewRat(1000*observed, 1)) != 0 {
					t.Fatalf("budget sparkline numeric input differs from independent minute population at%v", sample.EvaluationAt)
				}
			}
			if numeric == 0 {
				t.Fatal("history-ready cache contains no numeric budget samples")
			}

		})
	}
}
