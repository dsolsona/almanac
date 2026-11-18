//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceServiceMembershipVersionsAssignments(t *testing.T) {
	t.Parallel()
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
	stagedAt := moveAt.Add(-time.Minute)
	baseAuthority := maintenanceAuthorityRevision(t, f)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
		SELECT slo_id,2,$2,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,$3,$3
		FROM almanac_slo_definitions WHERE slo_id=$1 AND revision=1
	`, id, service.ID, stagedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_service_assignments WHERE slo_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("staged definition changed membership: count=%d err=%v", count, err)
	}
	if got := maintenanceAuthorityRevision(t, f); got != baseAuthority {
		t.Fatalf("staging advanced authority to %d, want %d", got, baseAuthority)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE almanac_slos SET definition_revision=2, updated_at=$2 WHERE id=$1`, id, moveAt); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE almanac_slos SET display_name='Renamed during selection', updated_at=$2 WHERE id=$1`, id, moveAt.Add(time.Second))
		return err
	})
	if got := maintenanceAuthorityRevision(t, f); got != baseAuthority+1 {
		t.Fatalf("selection advanced authority to %d, want %d", got, baseAuthority+1)
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
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_slos SET display_name='Renamed', updated_at=$2 WHERE id=$1`, id, moveAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := maintenanceAuthorityRevision(t, f); got != baseAuthority+1 {
		t.Fatalf("rename advanced authority to %d, want %d", got, baseAuthority+1)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_service_assignments WHERE slo_id=$1`, id).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rename changed membership: count=%d err=%v", count, err)
	}
}

func maintenanceAuthorityRevision(t *testing.T, f maintenanceFixture) int64 {
	t.Helper()
	revision, err := dbgen.New(f.conn).ObserveServiceMaintenanceAuthority(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return revision
}
