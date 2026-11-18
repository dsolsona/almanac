//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceCurrentnessFencesAuthority(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "current authority source")
	id := mustPGUUID(f.ids[0])
	if err := f.runtime.refreshCurrentProjection(t.Context(), id, f.clock); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(f.conn)
	key := dbgen.GetPublishedCurrentProjectionParams{SloID: id, Now: timestamp(f.now), CalculationVersion: operationalCalculationVersion}
	before, err := q.GetPublishedCurrentProjection(t.Context(), key)
	if err != nil || !before.Current || !before.Usable {
		t.Fatalf("baseline projection: %+v %v", before, err)
	}
	change := f.input("authority-after-current")
	change.Start, change.End = f.at.Add(-30*time.Minute), f.at.Add(-20*time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, change); err != nil {
		t.Fatal(err)
	}
	after, err := q.GetPublishedCurrentProjection(t.Context(), key)
	if err != nil || after.Current || after.Usable {
		t.Fatalf("old exclusion authority remained current/usable: current=%v usable=%v %v", after.Current, after.Usable, err)
	}
}

func TestIntegrationMaintenanceCurrentnessUsesMeasuredCutoff(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "current cutoff source")
	id := mustPGUUID(f.ids[0])
	q := dbgen.New(f.conn)
	definition, err := q.GetSlo(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	change := f.input("future-of-measured-cutoff")
	change.ScopeKind, change.SloIDs = MaintenanceScopeAllServices, nil
	change.Start, change.End = f.at, f.at.Add(time.Minute)
	f.now = f.at.Add(-30 * time.Second)
	firstWindow, err := CreateMaintenance(t.Context(), f.f.databaseURL, change)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(30 * time.Second)
	current, err := readCurrentSloFromAggregates(t.Context(), q, definition, f.now, nil)
	if err != nil || current.CurrentState == nil || *current.CurrentState != "ready" ||
		!current.Current.Window.End.Equal(f.at) {
		t.Fatalf("unedited measurements through cutoff %s were not ready: state=%v cutoff=%s err=%v", f.at, current.CurrentState, current.Current.Window.End, err)
	}

	if _, err := CancelMaintenance(t.Context(), f.f.databaseURL, MaintenanceCommandInput{
		Scoped: true, Proof: f.proof, ID: firstWindow.ID, Revision: firstWindow.Revision,
		Reason: "Fixture cancellation", IdempotencyKey: "cancel-measured-cutoff", RequestID: "cancel-measured-cutoff",
		Now: func() time.Time { return f.at.Add(-30 * time.Second) },
	}); err != nil {
		t.Fatal(err)
	}

	// Create an older pending window inside the stored measurement range but
	// outside the wall clock's equivalent lookback. The two cutoffs can differ
	// when the requested boundary advances before the measurement tail.
	f.now = f.at.Add(7 * time.Hour)
	if err := f.runtime.refreshCurrentProjection(t.Context(), id, f.clock); err != nil {
		t.Fatal(err)
	}
	actualEnd := f.now.Add(-2 * time.Minute)
	if _, err := f.conn.Exec(t.Context(), `
		UPDATE public.almanac_slo_current_projections
		SET payload=jsonb_set(payload,'{current,window,end}',to_jsonb($2::timestamptz))
		WHERE slo_id=$1
	`, id, actualEnd); err != nil {
		t.Fatal(err)
	}
	key := dbgen.GetPublishedCurrentProjectionParams{SloID: id, Now: timestamp(f.now), CalculationVersion: operationalCalculationVersion}
	before, err := q.GetPublishedCurrentProjection(t.Context(), key)
	if err != nil || !before.Current {
		t.Fatalf("stored cutoff fixture never became current: current=%t err=%v", before.Current, err)
	}
	change.IdempotencyKey = "older-measured-cutoff"
	change.Start, change.End = actualEnd.Add(-6*time.Hour), actualEnd.Add(-6*time.Hour+time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, change); err != nil {
		t.Fatal(err)
	}
	after, err := q.GetPublishedCurrentProjection(t.Context(), key)
	if err != nil || after.Current || !after.Usable {
		t.Fatalf("stored generation ignored pending measurement coverage: current=%t usable=%t err=%v", after.Current, after.Usable, err)
	}
}

func TestIntegrationMaintenanceCurrentnessDiscardsObsoletePreparation(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	q := dbgen.New(f.conn)
	id := mustPGUUID(f.ids[0])
	row, err := q.GetSlo(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	old := &maintenanceConsumerSession{
		requestedRevision: row.ExclusionRevision, sourceCaptured: true,
		scope:             maintenancePreparationScope{SloID: id, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision},
		preparation:       []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: row.ExclusionRevision}},
		preparationLimits: maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1},
	}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("after-current-prepare")); err != nil {
		t.Fatal(err)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		// Publication loses its revision fence after capture has created a
		// build. The caller should still be able to finish its fallback read.
		return publishCurrentMaintenancePreparation(t.Context(), tx, old, f.clock)
	})
	var builds, work int
	if err := f.conn.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM almanac_maintenance_builds WHERE slo_id=$1),
		       (SELECT count(*) FROM almanac_maintenance_preparation WHERE slo_id=$1)
	`, id).Scan(&builds, &work); err != nil || builds != 0 || work != 0 {
		t.Fatalf("obsolete read committed staging writes: builds=%d work=%d err=%v", builds, work, err)
	}
}
