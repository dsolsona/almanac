//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func preparationOptions(f maintenanceFixture) maintenancePreparationTurnOptions {
	return maintenancePreparationTurnOptions{MaxLive: 4, MaxHistorical: 4, Concurrency: 2, PageLimit: 1, Lease: time.Minute, OperationBudget: 5 * time.Second, PendingRetry: time.Second, FailureRetry: 2 * time.Second, MaxAttempts: 3, RetentionStart: f.historyStart}
}
func TestIntegrationMaintenancePreparationWorkerFixedRevision(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	m := f.input("first")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 1}}, maintenancePreparationLive)
	m = f.input("second")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 2}}, maintenancePreparationHistorical)
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	options := preparationOptions(f)
	options.MaxLive = 1
	options.MaxHistorical = 1
	options.Concurrency = 1
	now := f.now
	first, err := runtime.runMaintenancePreparationTurn(t.Context(), now, options, func() time.Time { return now })
	if err != nil || first.Claimed != 2 || first.Pending != 2 {
		t.Fatalf("bounded live/history turn: %+v %v", first, err)
	}
	for i := 1; i < 8; i++ {
		now = f.now.Add(time.Duration(i) * time.Second)
		if _, err = runtime.runMaintenancePreparationTurn(t.Context(), now, options, func() time.Time { return now }); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation WHERE state='complete' AND attempts=0`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("build jobs complete %d %v", count, err)
	}
	var cursor int64
	if err = f.conn.QueryRow(t.Context(), `SELECT raw_cursor FROM almanac_maintenance_builds WHERE slo_id=$1 AND target_revision=1`, f.ids[0]).Scan(&cursor); err != nil || cursor != 1 {
		t.Fatalf("historical target grew to current: %d %v", cursor, err)
	}
}
func TestIntegrationMaintenancePreparationWorkerScopedAndComparison(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	m := f.input("nested")
	m.Start = h.HourStart.Add(12 * time.Minute)
	m.End = h.HourStart.Add(15 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{
		{Kind: "scoped_hour", RequestedRevision: 1, BuildID: h.BuildID, Hour: h.maintenanceHourKey},
		{Kind: "comparison", RequestedRevision: 2, Comparison: &maintenanceEvaluationBuildPair{FirstBuild: h.BuildID, SecondBuild: b.ID}},
	}, maintenancePreparationLive)
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	options := preparationOptions(f)
	for i := range 4 {
		now := f.now.Add(time.Duration(i) * time.Second)
		if _, err = runtime.runMaintenancePreparationTurn(t.Context(), now, options, func() time.Time { return now }); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation WHERE state='complete'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("resource outcomes %d %v", count, err)
	}
	var equal bool
	if err = f.conn.QueryRow(t.Context(), `SELECT state='equal' FROM almanac_maintenance_comparisons`).Scan(&equal); err != nil || !equal {
		t.Fatalf("exact comparison %v %v", equal, err)
	}
	row, err := dbgen.New(f.conn).ReadMaintenanceHour(t.Context(), dbgen.ReadMaintenanceHourParams{SloID: h.SloID, DefinitionRevision: 1, MeasurementRevision: 1, BuildID: h.BuildID, HourStart: timestamp(h.HourStart), SourceGeneration: h.SourceGeneration})
	if err != nil || row.EligibleObserved != 50 || row.ExcludedObserved != 10 {
		t.Fatalf("actual scoped hour %+v %v", row, err)
	}
}
func TestIntegrationMaintenancePreparationWorkerCompatibilityFixed(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes DISABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	m := f.input("legacy-one")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "compatibility", RequestedRevision: 1}}, maintenancePreparationLive)
	m = f.input("legacy-two")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes ENABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err = runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock); err != nil {
		t.Fatal(err)
	}
	var through int64
	if err = f.conn.QueryRow(t.Context(), `SELECT indexed_through FROM almanac_maintenance_compatibility WHERE slo_id=$1`, f.ids[0]).Scan(&through); err != nil || through != 1 {
		t.Fatalf("fixed compatibility target changed %d %v", through, err)
	}
}
func TestIntegrationMaintenancePreparationPublicationBeforeAck(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 0}}, maintenancePreparationLive)
	var claim dbgen.AlmanacMaintenancePreparation
	compilerTx(t, f, func(tx pgx.Tx) error {
		rows, err := claimMaintenancePreparation(t.Context(), tx, maintenancePreparationLive, f.now, time.Minute, 1, f.clock)
		if err == nil {
			claim = rows[0]
		}
		return err
	})
	// Simulate a valid shared resource publication followed by process death before ack.
	compilerFinish(t, f, claim.BuildID, 10)
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	later := f.now.Add(2 * time.Minute)
	got, err := runtime.runMaintenancePreparationTurn(t.Context(), later, preparationOptions(f), func() time.Time { return later })
	if err != nil || got.Complete != 1 {
		t.Fatalf("published resource replay: %+v %v", got, err)
	}
}
