//go:build integration

package storage

import (
	"errors"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"testing"
	"time"
)

func TestIntegrationMaintenanceHistoryFutureReuse(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "legacy retained source")
	if _, err := f.runtime.buildSloExtrema(t.Context(), f.clock); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("future-retained-reuse")); err != nil {
		t.Fatal(err)
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	start, end := f.historyStart, f.historyStart.Add(time.Hour)
	got, err := ReadSloOperationalDetail(t.Context(), f.f.databaseURL, OperationalDetailInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], At: &f.at, Start: &start, End: &end, BurnWindow: 3600, Now: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation == nil || got.Generation.ExclusionRevision != "1" || got.HistoryPreparation != nil || len(got.BudgetSeries.Samples) == 0 {
		t.Fatalf("future edit blocked or relabelled history: generation=%+v preparation=%+v samples=%d", got.Generation, got.HistoryPreparation, len(got.BudgetSeries.Samples))
	}
	for _, sample := range got.BudgetSeries.Samples {
		if sample.Total != nil && (sample.CalculationProvenance == nil || sample.CalculationProvenance.ComputedRevision != "0" || sample.CalculationProvenance.RequestedRevision != "1" || sample.CalculationProvenance.Compatibility == nil || sample.CalculationProvenance.Compatibility.Kind != "indexed_absence") {
			t.Fatalf("retained numeric sample lost actual canonical identity/proof: %+v", sample)
		}
	}
	var builds, changedRows int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM public.almanac_maintenance_builds WHERE slo_id=$1 AND target_revision=1),(SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1 AND exclusion_revision<>0)`, f.ids[0]).Scan(&builds, &changedRows); err != nil || builds != 0 || changedRows != 0 {
		t.Fatalf("unaffected history unnecessarily rebuilt: builds=%d rows=%d %v", builds, changedRows, err)
	}
}

func TestIntegrationMaintenanceHistoryTerminalTarget(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{"unavailable", "exhausted"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			f.drain(t, "original terminal fixture source")
			if _, err := f.runtime.buildSloExtrema(t.Context(), f.clock); err != nil {
				t.Fatal(err)
			}
			change := f.input("terminal-retained-hour")
			change.Start, change.End = f.historyStart.Add(10*time.Minute), f.historyStart.Add(20*time.Minute)
			if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, change); err != nil {
				t.Fatal(err)
			}
			build := compilerCapture(t, f, nil)
			compilerFinish(t, f, build.ID, 10)
			f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
			start, end := f.historyStart, f.historyStart.Add(time.Hour)
			read := func() api.SloOperationalDetail {
				t.Helper()
				got, err := ReadSloOperationalDetail(t.Context(), f.f.databaseURL, OperationalDetailInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], At: &f.at, Start: &start, End: &end, BurnWindow: 3600, Now: f.clock})
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			for range 20 {
				read()
				var target pgtype.UUID
				err := f.conn.QueryRow(t.Context(), `SELECT id FROM almanac_maintenance_preparation WHERE slo_id=$1 AND kind='extrema_hour' AND state='pending' LIMIT 1`, f.ids[0]).Scan(&target)
				if err == nil {
					if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state=$2,reason='history_unavailable' WHERE id=$1`, target, terminal); err != nil {
						t.Fatal(err)
					}
					f.now = f.now.Add(time.Second)
					got := read()
					if got.HistoryPreparation == nil || !got.HistoryPreparation.Failed {
						t.Fatalf("terminal retained target remained endlessly pending: %+v", got.HistoryPreparation)
					}
					for _, sample := range got.BudgetSeries.Samples {
						if sample.Quality.Reason != nil && *sample.Quality.Reason == "history_preparation_failed" && sample.Total == nil {
							return
						}
					}
					t.Fatal("terminal retained target omitted its explicit gap")
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal(err)
				}
				if _, err := f.runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock); err != nil {
					t.Fatal(err)
				}
				f.now = f.now.Add(time.Second)
			}
			t.Fatal("fixture did not reach the targeted repair stage")
		})
	}
}

func TestIntegrationMaintenanceHistoryEdgeProvenance(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "retained edge source")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("future-edge-provenance")); err != nil {
		t.Fatal(err)
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	start, end := f.historyStart.Add(15*time.Minute), f.historyStart.Add(45*time.Minute)
	got, err := ReadSloOperationalDetail(t.Context(), f.f.databaseURL, OperationalDetailInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], At: &f.at, Start: &start, End: &end, BurnWindow: 3600, Now: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	for _, series := range []api.SloOperationalSeries{got.BudgetSeries, got.BurnSeries, got.AttainmentSeries} {
		if len(series.Samples) == 0 {
			t.Fatalf("missing live-edge %s samples", series.Kind)
		}
		for _, sample := range series.Samples {
			if sample.Total != nil && (sample.CalculationProvenance == nil || sample.CalculationProvenance.RequestedRevision != "1" || sample.CalculationProvenance.ComputedRevision != "0") {
				t.Fatalf("edge projection dropped actual maintenance identity: kind=%s sample=%+v", series.Kind, sample)
			}
		}
	}
}

func TestIntegrationMaintenanceHistoryChangedBucketRecovers(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "original retained source")
	if _, err := f.runtime.buildSloExtrema(t.Context(), f.clock); err != nil {
		t.Fatal(err)
	}
	change := f.input("changed-retained-hour")
	change.Start, change.End = f.historyStart.Add(10*time.Minute), f.historyStart.Add(20*time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, change); err != nil {
		t.Fatal(err)
	}
	build := compilerCapture(t, f, nil)
	compilerFinish(t, f, build.ID, 10)
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	start, end := f.historyStart, f.historyStart.Add(time.Hour)
	read := func() api.SloOperationalDetail {
		t.Helper()
		got, err := ReadSloOperationalDetail(t.Context(), f.f.databaseURL, OperationalDetailInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], At: &f.at, Start: &start, End: &end, BurnWindow: 3600, Now: f.clock})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := read()
	if first.HistoryPreparation == nil {
		t.Fatal("changed historical inputs were presented as ready")
	}
	for step := range 20 {
		if _, err := f.runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock); err != nil {
			t.Fatalf("preparation step%d: %v", step, err)
		}
		f.now = f.now.Add(time.Second)
		got := read()
		if got.HistoryPreparation != nil {
			continue
		}
		for _, sample := range got.BudgetSeries.Samples {
			if sample.EvaluationAt.Equal(end) {
				if sample.Total == nil || *sample.Total != "50000.000000000000" || sample.Good == nil || *sample.Good != "49500.000000000000" || sample.Quality.ExcludedIntervals == nil || *sample.Quality.ExcludedIntervals != 10 || sample.CalculationProvenance == nil || sample.CalculationProvenance.ComputedRevision != "1" || sample.CalculationProvenance.RequestedRevision != "1" {
					t.Fatalf("repaired graph diverges from raw counts: %+v", sample)
				}
				var rows int
				if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1 AND exclusion_revision=1`, f.ids[0]).Scan(&rows); err != nil || rows != 1 {
					t.Fatalf("repair rebuilt unrelated buckets: rows=%d %v", rows, err)
				}
				return
			}
		}
		t.Fatal("ready graph lost the last real endpoint")
	}
	t.Fatal("finite targeted prerequisites never repaired the historical hour")
}
