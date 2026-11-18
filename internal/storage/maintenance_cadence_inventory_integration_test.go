//go:build integration

package storage

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationMaintenanceCadenceInventoryFollowsActiveDefinitions(t *testing.T) {
	f := newMaintenanceFixture(t)
	q := dbgen.New(f.conn)
	initial, err := q.GetSlo(t.Context(), mustPGUUID(f.ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "new-maintenance-grid", DisplayName: "New grid",
		IdempotencyKey: "grid", RequestID: "grid", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	newService := mustPGUUID(other.ID)
	check := func(all bool, services []pgtype.UUID, expected ...int32) {
		t.Helper()
		got, err := q.ListServiceMaintenanceCadences(t.Context(), dbgen.ListServiceMaintenanceCadencesParams{
			AllServices: all, ServiceIds: services,
		})
		if err != nil || !slices.Equal(got, expected) {
			t.Fatalf("cadences = %v, want %v (%v)", got, expected, err)
		}
	}
	var initialCount int
	if err := f.conn.QueryRow(t.Context(), `SELECT slo_count FROM almanac_maintenance_installation_grids WHERE cadence_seconds = 60`).Scan(&initialCount); err != nil || initialCount != 2 {
		t.Fatalf("initial grid count = %d, want 2 (%v)", initialCount, err)
	}
	checkCount := func(service pgtype.UUID, cadence int32, expected int64) {
		t.Helper()
		var got int64
		if err := f.conn.QueryRow(t.Context(), `
			SELECT CASE WHEN $1::uuid IS NULL THEN
				coalesce((SELECT slo_count FROM almanac_maintenance_installation_grids WHERE cadence_seconds=$2),0)
			ELSE coalesce((SELECT slo_count FROM almanac_maintenance_service_grids WHERE service_id=$1 AND cadence_seconds=$2),0) END
		`, service, cadence).Scan(&got); err != nil || got != expected {
			t.Fatalf("grid count service=%v cadence=%d got=%d want=%d err=%v", service, cadence, got, expected, err)
		}
	}
	check(true, nil, 60)
	check(false, []pgtype.UUID{initial.ServiceID}, 60)
	check(false, []pgtype.UUID{newService})
	moved := f.at.Add(time.Minute)
	if _, err := f.conn.Exec(t.Context(), `
		INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
		SELECT slo_id,2,$2,sli_id,measurement_revision,target,rolling_window_seconds,90,$3,$3
		FROM almanac_slo_definitions WHERE slo_id=$1 AND revision=1
	`, f.ids[0], newService, moved); err != nil {
		t.Fatal(err)
	}
	check(true, nil, 60)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slos SET definition_revision=2, updated_at=$2 WHERE id=$1`, f.ids[0], moved); err != nil {
		t.Fatal(err)
	}
	check(true, nil, 60, 90)
	check(false, []pgtype.UUID{initial.ServiceID}, 60)
	check(false, []pgtype.UUID{newService}, 90)
	checkCount(pgtype.UUID{}, 60, 1)
	checkCount(pgtype.UUID{}, 90, 1)
	checkCount(initial.ServiceID, 60, 1)
	checkCount(newService, 90, 1)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_services SET archived_at=$2 WHERE id=$1`, newService, moved.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	check(true, nil, 60)
	check(false, []pgtype.UUID{newService})
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_services SET archived_at=NULL WHERE id=$1`, newService); err != nil {
		t.Fatal(err)
	}
	check(true, nil, 60, 90)
	common := f.at.Truncate(3 * time.Minute).Add(3 * time.Hour)
	request := f.input("misaligned-to-current-grids")
	request.Scoped, request.ScopeKind, request.SloIDs = true, MaintenanceScopeAllServices, nil
	request.Start, request.End = common.Add(time.Minute), common.Add(2*time.Minute)
	_, err = CreateMaintenance(t.Context(), f.f.databaseURL, request)
	alignment, ok := errors.AsType[*MaintenanceAlignmentError](err)
	if !ok || !alignment.SuggestedStart.Equal(common) || !alignment.SuggestedEnd.Equal(common.Add(3*time.Minute)) {
		t.Fatalf("should suggest a boundary covering both grids: %+v (%v)", alignment, err)
	}
	moveMaintenanceSLOAtCadence(t, f, other.ID, 3, moved.Add(2*time.Minute), 120)
	checkCount(pgtype.UUID{}, 90, 0)
	checkCount(pgtype.UUID{}, 120, 1)
	checkCount(newService, 90, 0)
	checkCount(newService, 120, 1)
}

func TestIntegrationMaintenanceCadenceEarlyEndPreservesOriginalStart(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	common := f.at.Truncate(3 * time.Minute).Add(3 * time.Minute)
	input := f.input("early-end-grid")
	input.Scoped, input.ScopeKind, input.SloIDs = true, MaintenanceScopeAllServices, nil
	input.Start, input.End = common.Add(time.Minute), common.Add(5*time.Minute)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}

	// An SLO starts using a 90-second grid after the 60-second window opens.
	f.now = input.Start.Add(40 * time.Second)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
		SELECT slo_id,2,service_id,sli_id,measurement_revision,target,rolling_window_seconds,90,$2,$2
		FROM almanac_slo_definitions WHERE slo_id=$1 AND revision=1
	`, f.ids[0], f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE almanac_slos SET definition_revision=2, updated_at=$2 WHERE id=$1`, f.ids[0], f.now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	end := EndMaintenanceInput{
		Scoped: true, Proof: f.proof, ID: window.ID, Revision: window.Revision,
		End: common.Add(4 * time.Minute), Reason: "Done", IdempotencyKey: "end-grid", RequestID: "end-grid", Now: f.clock,
	}
	_, err = EndMaintenance(t.Context(), f.f.databaseURL, end)
	alignment, ok := errors.AsType[*MaintenanceAlignmentError](err)
	if !ok || !alignment.SuggestedStart.Equal(window.Start) || !alignment.SuggestedEnd.Equal(common.Add(3*time.Minute)) {
		t.Fatalf("early end suggestion = %+v (%v), want original start and previous prospective boundary", alignment, err)
	}

	f.now = common.Add(3*time.Minute + 10*time.Second)
	_, err = EndMaintenance(t.Context(), f.f.databaseURL, end)
	alignment, ok = errors.AsType[*MaintenanceAlignmentError](err)
	if !ok || !alignment.SuggestedStart.IsZero() || !alignment.SuggestedEnd.IsZero() {
		t.Fatalf("early end suggested an unavailable boundary: %+v (%v)", alignment, err)
	}

	f.now = input.Start.Add(40 * time.Second)
	end.End = common.Add(3 * time.Minute)
	shortened, err := EndMaintenance(t.Context(), f.f.databaseURL, end)
	if err != nil || !shortened.Start.Equal(window.Start) || !shortened.End.Equal(end.End) {
		t.Fatalf("suggested early end was rejected: %+v (%v)", shortened, err)
	}
}

func TestIntegrationMaintenanceCadenceHistoricalSelectorChecksDepartedGrid(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	q := dbgen.New(f.conn)
	original, err := q.GetSlo(t.Context(), mustPGUUID(f.ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "retired-grid-service", DisplayName: "Retired grid destination",
		IdempotencyKey: "retired-grid-service", RequestID: "retired-grid-service", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	base := f.at.Truncate(2 * time.Minute).Add(2 * time.Minute)
	moveMaintenanceSLOAtCadence(t, f, original.ServiceID.String(), 2, base, 120)
	moveMaintenanceSLOAtCadence(t, f, other.ID, 3, base.Add(4*time.Minute), 120)
	f.now = base.Add(5 * time.Minute)
	input := f.input("historical-selector-grid")
	input.ScopeKind, input.SloIDs, input.ServiceIDs = MaintenanceScopeServices, nil, []string{original.ServiceID.String()}
	input.Start, input.End = base.Add(2*time.Minute), base.Add(3*time.Minute)
	_, err = CreateMaintenance(t.Context(), f.f.databaseURL, input)
	alignment, ok := errors.AsType[*MaintenanceAlignmentError](err)
	if !ok || !alignment.SuggestedStart.Equal(input.Start) || !alignment.SuggestedEnd.Equal(base.Add(4*time.Minute)) {
		t.Fatalf("historical selector failed to require its former grid: %+v (%v)", alignment, err)
	}
	input.End = alignment.SuggestedEnd
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatalf("common historical bounds rejected: %v", err)
	}
}

func TestIntegrationMaintenanceCadenceCountsCommittedDefinitionOnly(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	var id string
	compilerTx(t, f, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO almanac_slos(id, name, display_name, created_at, updated_at)
			VALUES(gen_random_uuid(), 'final-commit-grid', 'Final commit grid', $1, $1)
			RETURNING id::text
		`, f.at).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
			INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
			SELECT $2::uuid,2,service_id,sli_id,measurement_revision,target,rolling_window_seconds,90,activated_at,activated_at
			FROM almanac_slo_definitions WHERE slo_id=$1 AND revision=1
		`, f.ids[0], id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE almanac_slos SET definition_revision=2 WHERE id=$1`, id)
		return err
	})
	var assignments, finalGridCount, originalGridCount int
	if err := f.conn.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM almanac_slo_service_assignments WHERE slo_id=$1 AND definition_revision=2),
		       (SELECT slo_count FROM almanac_maintenance_installation_grids WHERE cadence_seconds=90),
		       (SELECT slo_count FROM almanac_maintenance_installation_grids WHERE cadence_seconds=60)
	`, id).Scan(&assignments, &finalGridCount, &originalGridCount); err != nil || assignments != 1 || finalGridCount != 1 || originalGridCount != 2 {
		t.Fatalf("final grid counted incorrectly: assignments=%d new=%d old=%d err=%v", assignments, finalGridCount, originalGridCount, err)
	}
}
