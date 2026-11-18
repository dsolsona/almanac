//go:build integration

package storage

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
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
