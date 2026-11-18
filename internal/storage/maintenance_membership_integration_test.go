//go:build integration

package storage

import (
	"testing"
	"time"
)

func TestIntegrationMaintenanceServiceMembershipVersionsAssignments(t *testing.T) {
	f := newMaintenanceFixture(t)
	id := f.ids[0]
	var expectedService string
	if err := f.conn.QueryRow(t.Context(), `SELECT id FROM almanac_services WHERE name='maintenance-a-service'`).Scan(&expectedService); err != nil {
		t.Fatal(err)
	}
	var original string
	var initialAt time.Time
	if err := f.conn.QueryRow(t.Context(), `
		SELECT service_id, effective_at
		FROM almanac_slo_service_assignments WHERE slo_id=$1 AND definition_revision=1
	`, id).Scan(&original, &initialAt); err != nil {
		t.Fatal(err)
	}
	if original != expectedService || !initialAt.Equal(f.at) {
		t.Fatalf("initial membership = %s at %s, want %s at %s", original, initialAt, expectedService, f.at)
	}

	service, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "move-destination", DisplayName: "Move destination",
		IdempotencyKey: "move-destination", RequestID: "move-destination", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	moveAt := initialAt.Add(30*time.Minute + 3*time.Second)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
		SELECT slo_id,2,$2,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,$3,$3
		FROM almanac_slo_definitions WHERE slo_id=$1 AND revision=1
	`, id, service.ID, moveAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_service_assignments WHERE slo_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("staged definition changed membership: count=%d err=%v", count, err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slos SET definition_revision=2, updated_at=$2 WHERE id=$1`, id, moveAt); err != nil {
		t.Fatal(err)
	}
	var current string
	var currentAt time.Time
	if err := f.conn.QueryRow(t.Context(), `
		SELECT service_id, effective_at
		FROM almanac_slo_service_assignments WHERE slo_id=$1 AND definition_revision=2
	`, id).Scan(&current, &currentAt); err != nil {
		t.Fatal(err)
	}
	if current != service.ID || !currentAt.Equal(moveAt) || current == original {
		t.Fatalf("prospective move = %s at %s, want %s at %s", current, currentAt, service.ID, moveAt)
	}

	// Renaming an objective does not generate a membership boundary.
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slos SET display_name='Renamed', updated_at=$2 WHERE id=$1`, id, moveAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_service_assignments WHERE slo_id=$1`, id).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rename changed membership: count=%d err=%v", count, err)
	}
}
