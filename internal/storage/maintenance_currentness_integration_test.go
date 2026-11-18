//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	f.now = f.at.Add(90 * time.Second)
	current, err := readCurrentSloFromAggregates(t.Context(), q, definition, f.now, nil)
	if err != nil || current.CurrentState == nil || *current.CurrentState != "ready" ||
		!current.Current.Window.End.Equal(f.at) {
		t.Fatalf("unedited measurements through cutoff %s were not ready: state=%v cutoff=%s err=%v", f.at, current.CurrentState, current.Current.Window.End, err)
	}

	if tag, err := execObservationFixture(t, f.conn, f.sliID, `
INSERT INTO almanac_sli_observations
 (interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
  execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT $2::timestamptz,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
       gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,$2,$3
FROM almanac_sli_observations
WHERE sli_id=$1 AND interval_start=$2::timestamptz-interval '1 minute'`, f.sliID, f.at, f.now); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("advance measured cutoff: wrote %d observations: %v", tag.RowsAffected(), err)
	}
	if _, err := readCurrentSloFromAggregates(t.Context(), q, definition, f.now, nil); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("live read missed pending maintenance after extending the measured range: %v", err)
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

func TestIntegrationMaintenanceCurrentnessWaitsForMeasuredObservationWork(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "measured observation work source")
	ctx := t.Context()
	id := mustPGUUID(f.ids[0])
	q := dbgen.New(f.conn)
	definition, err := q.GetSlo(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	requested := f.at.Add(90 * time.Second)
	current, err := readCurrentSloFromAggregates(ctx, q, definition, requested, nil)
	if err != nil || current.CurrentState == nil || *current.CurrentState != "ready" || !current.Current.Window.End.Equal(f.at) {
		t.Fatalf("baseline measured cutoff: state=%v cutoff=%s err=%v", current.CurrentState, current.Current.Window.End, err)
	}

	// Pick a complete interior hour so pending work cannot be handled as raw edges.
	hour := f.historyStart.Truncate(time.Hour).Add(time.Hour)
	var observed int64
	if err := f.conn.QueryRow(ctx, `
SELECT observed_intervals FROM almanac_sli_hourly_aggregates
WHERE sli_id=$1 AND measurement_revision=$2 AND hour_start=$3`, f.sliID, definition.MeasurementRevision, hour).Scan(&observed); err != nil || observed != 60 {
		t.Fatalf("fixture hour %s contains %d observed minutes: %v", hour, observed, err)
	}
	tag, err := execObservationFixture(t, f.conn, f.sliID, `
UPDATE almanac_sli_observations SET ingestion_generation=0
WHERE sli_id=$1 AND interval_start=$2 AND measurement_revision=$3 AND outcome='observed'`,
		f.sliID, hour, definition.MeasurementRevision)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("mark measured source hour dirty: touched %d observations: %v", tag.RowsAffected(), err)
	}
	if err := requireServiceMaintenanceReady(ctx, q, id, f.at.Add(-6*time.Hour), f.at); err != nil {
		t.Fatalf("unexpected maintenance work in the measured range: %v", err)
	}
	if _, err := readCurrentSloFromAggregates(ctx, q, definition, requested, nil); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("live read missed pending observation work inside a fully measured hour: %v", err)
	}
}

func TestIntegrationMaintenanceCurrentnessDiscardsObsoletePreparation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		changed       bool
		rejectQueue   bool
		wantArtifacts int
	}{
		{name: "unchanged", wantArtifacts: 1},
		{name: "changed", changed: true},
		{name: "queue insert fails", rejectQueue: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			ctx := t.Context()
			if _, err := CreateMaintenance(ctx, f.f.databaseURL, f.input("before-current-prepare")); err != nil {
				t.Fatal(err)
			}
			q := dbgen.New(f.conn)
			id := mustPGUUID(f.ids[0])
			row, err := q.GetSlo(ctx, id)
			if err != nil || row.ExclusionRevision < 1 {
				t.Fatalf("expected nonzero exclusion revision: %+v %v", row, err)
			}
			generation, err := q.GetDerivedSourceGeneration(ctx, dbgen.GetDerivedSourceGenerationParams{
				SliID: row.SliID, MeasurementRevision: row.MeasurementRevision,
			})
			if err != nil {
				t.Fatal(err)
			}
			session := &maintenanceConsumerSession{
				requestedRevision: row.ExclusionRevision, sourceCaptured: true, sourceGeneration: generation,
				scope:             maintenancePreparationScope{SloID: id, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision},
				preparation:       []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: row.ExclusionRevision}},
				preparationLimits: maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1},
			}
			if tc.changed {
				if _, err := CreateMaintenance(ctx, f.f.databaseURL, f.input("after-current-prepare")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.rejectQueue {
				if _, err := f.conn.Exec(ctx, `ALTER TABLE almanac_maintenance_preparation ADD CONSTRAINT fixture_reject_current_mask CHECK(kind<>'mask')`); err != nil {
					t.Fatal(err)
				}
			}
			compilerTx(t, f, func(tx pgx.Tx) error {
				err := publishCurrentMaintenancePreparation(ctx, tx, session, f.clock)
				if !tc.rejectQueue {
					return err
				}
				failure, ok := errors.AsType[*pgconn.PgError](err)
				if !ok || failure.Code != "23514" || failure.ConstraintName != "fixture_reject_current_mask" {
					t.Fatalf("wanted the injected preparation queue error, got %v", err)
				}
				return nil
			})
			var builds, work int
			err = f.conn.QueryRow(ctx, `
				SELECT (SELECT count(*) FROM almanac_maintenance_builds WHERE slo_id=$1),
				       (SELECT count(*) FROM almanac_maintenance_preparation WHERE slo_id=$1)
			`, id).Scan(&builds, &work)
			if err != nil || builds != tc.wantArtifacts || work != tc.wantArtifacts {
				t.Fatalf("unexpected staging writes: builds=%d work=%d err=%v", builds, work, err)
			}
		})
	}
}
