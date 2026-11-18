//go:build integration

package storage

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationMaintenanceServiceSelectorAuthority(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	authority := maintenanceAuthorityRevision(t, f)
	objective, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	input := f.input("selected-service")
	input.ScopeKind, input.SloIDs, input.ServiceIDs = MaintenanceScopeServices, nil, []string{objective.ServiceID.String()}
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil || window.ScopeKind != MaintenanceScopeServices || !slices.Equal(window.ServiceIDs, input.ServiceIDs) || len(window.SloIDs) != 0 {
		t.Fatalf("persisted selector = %+v, err = %v", window, err)
	}
	if got := maintenanceAuthorityRevision(t, f); got != authority+1 {
		t.Fatalf("creation advanced authority to %d, want %d", got, authority+1)
	}
	replay, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil || replay.ID != window.ID {
		t.Fatalf("replay selector = %+v, err = %v", replay, err)
	}

	if got := maintenanceAuthorityRevision(t, f); got != authority+1 {
		t.Fatalf("replay advanced authority to %d, want %d", got, authority+1)
	}

	if _, err := ReadMaintenance(t.Context(), f.f.databaseURL, ReadMaintenanceInput{
		Proof: f.proof, ID: window.ID, Now: f.clock,
	}); !errors.Is(err, ErrMaintenanceNotFound) {
		t.Fatalf("legacy read of broad selector: %v", err)
	}
	legacy, err := ListMaintenance(t.Context(), f.f.databaseURL, ListMaintenanceInput{Proof: f.proof, Now: f.clock})
	if err != nil || len(legacy.Items) != 0 {
		t.Fatalf("legacy list exposed broad selector: %+v, %v", legacy, err)
	}
	scoped, err := ListMaintenance(t.Context(), f.f.databaseURL, ListMaintenanceInput{Scoped: true, Proof: f.proof, Now: f.clock})
	if err != nil || len(scoped.Items) != 1 || scoped.Items[0].ID != window.ID {
		t.Fatalf("scoped list = %+v, %v", scoped, err)
	}
	var desiredRevision, exclusionRevision int64
	var complete bool
	if err := f.conn.QueryRow(t.Context(), `
		SELECT p.desired_revision, p.complete, s.revision FROM almanac_service_maintenance_projection p
		CROSS JOIN almanac_slo_exclusion_state s WHERE p.exclusion_id=$1 AND s.slo_id=$2
	`, window.ID, f.ids[0]).Scan(&desiredRevision, &complete, &exclusionRevision); err != nil || complete || desiredRevision != 1 || exclusionRevision != 0 {
		t.Fatalf("scheduling fanned out before commit: revision=%d complete=%t exclusion=%d err=%v", desiredRevision, complete, exclusionRevision, err)
	}
	witness := serviceSuppressionAt(t, f, f.ids[0], window.Start)
	if witness == nil || witness.ID != window.ID || witness.Revision != 1 || !witness.Start.Equal(window.Start) || !witness.End.Equal(window.End) {
		t.Fatalf("scheduled boundary missing direct suppression witness: %+v", witness)
	}
	if witness := serviceSuppressionAt(t, f, f.ids[0], window.End); witness != nil {
		t.Fatalf("exclusive end was suppressed: %+v", witness)
	}

	cancel := MaintenanceCommandInput{Proof: f.proof, ID: window.ID, Revision: 1, Reason: "Cancelled", IdempotencyKey: "cancel", RequestID: "cancel", Now: f.clock}
	if _, err := CancelMaintenance(t.Context(), f.f.databaseURL, cancel); !errors.Is(err, ErrMaintenanceNotFound) {
		t.Fatalf("legacy cancellation of broad selector: %v", err)
	}
	cancel.Scoped = true
	if cancelled, err := CancelMaintenance(t.Context(), f.f.databaseURL, cancel); err != nil || !cancelled.Tombstone || cancelled.Revision != 2 {
		t.Fatalf("scoped cancellation = %+v, %v", cancelled, err)
	}
	if got := maintenanceAuthorityRevision(t, f); got != authority+2 {
		t.Fatalf("cancellation advanced authority to %d, want %d", got, authority+2)
	}
	if witness := serviceSuppressionAt(t, f, f.ids[0], window.Start); witness != nil {
		t.Fatalf("cancelled window suppressed the future boundary: %+v", witness)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT desired_revision FROM almanac_service_maintenance_projection WHERE exclusion_id=$1`, window.ID).Scan(&desiredRevision); err != nil || desiredRevision != 2 {
		t.Fatalf("old projection target survived cancellation: revision=%d err=%v", desiredRevision, err)
	}
}

func serviceSuppressionAt(t *testing.T, f maintenanceFixture, sloID string, cutoff time.Time) *maintenanceSuppressionEvidence {
	t.Helper()
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	id := mustPGUUID(sloID)
	if _, err := dbgen.New(tx).FenceMaintenanceState(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	witness, err := readMaintenanceSuppression(t.Context(), tx, id, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	return witness
}

func TestIntegrationMaintenanceServicePreviewIsPaged(t *testing.T) {
	f := newMaintenanceFixture(t)
	objective, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	preview := MaintenanceScopePreviewInput{
		Proof: f.proof, ScopeKind: MaintenanceScopeServices, ServiceIDs: []string{objective.ServiceID.String()}, Limit: 1, Now: f.clock,
	}
	first, err := PreviewMaintenanceScope(t.Context(), f.f.databaseURL, preview)
	if err != nil || !first.Ready || !first.InventoryAt.Equal(f.at) || first.Total != 2 || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first preview = %+v, err = %v", first, err)
	}
	preview.Cursor = first.NextCursor
	second, err := PreviewMaintenanceScope(t.Context(), f.f.databaseURL, preview)
	if err != nil || second.Total != 2 || len(second.Items) != 1 || second.NextCursor != "" || !second.InventoryAt.Equal(first.InventoryAt) ||
		second.Items[0].SloID == first.Items[0].SloID {
		t.Fatalf("next preview page = %+v, err = %v", second, err)
	}
	// Commit the join at the same wall-clock instant as the first inventory.
	// A time-only cursor includes this new SLO in the second page.
	_, _, err = CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "third-preview-slo", DisplayName: "Third SLO", ServiceID: objective.ServiceID.String(),
		SliID: f.sliID, Target: "0.99", RollingWindowSeconds: 3600, IdempotencyKey: "third-preview-slo", RequestID: "third-preview-slo", Now: f.clock,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := PreviewMaintenanceScope(t.Context(), f.f.databaseURL, preview); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("continuation mixed membership after same-time join: %+v, err = %v", changed, err)
	}
	preview.Cursor = ""
	latest, err := PreviewMaintenanceScope(t.Context(), f.f.databaseURL, preview)
	if err != nil || latest.Total != 3 || !latest.InventoryAt.Equal(f.now) {
		t.Fatalf("fresh preview missed join: %+v, err = %v", latest, err)
	}
	empty, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "empty-preview-service", DisplayName: "Empty service",
		IdempotencyKey: "empty-preview-service", RequestID: "empty-preview-service", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	preview.ServiceIDs = []string{empty.ID}
	members, err := PreviewMaintenanceScope(t.Context(), f.f.databaseURL, preview)
	if err != nil || !members.Ready || members.Total != 0 || len(members.Items) != 0 {
		t.Fatalf("empty service inventory = %+v, err = %v", members, err)
	}
	preview.ServiceIDs = []string{"d4eef428-1a55-4343-91b3-dfc944f51a0b"}
	if _, err := PreviewMaintenanceScope(t.Context(), f.f.databaseURL, preview); !errors.Is(err, ErrMaintenanceNotFound) {
		t.Fatalf("missing service looked like empty inventory: %v", err)
	}
}

func TestIntegrationMaintenanceServiceSuppressionUsesRevisionOrder(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.now = f.at.Add(time.Minute)
	input := f.input("revision-order")
	input.ScopeKind, input.SloIDs = MaintenanceScopeAllServices, nil
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}

	if witness := serviceSuppressionAt(t, f, f.ids[0], window.Start); witness == nil || witness.ID != window.ID || witness.Revision != window.Revision {
		t.Fatalf("all-services scope lacked a witness before cancellation: %+v", witness)
	}

	// Aggregate revision remains monotonic across separate authorized requests
	// even if the installation's wall clock moves backward between them.
	f.now = f.at
	cancel, err := CancelMaintenance(t.Context(), f.f.databaseURL, MaintenanceCommandInput{
		Scoped: true, Proof: f.proof, ID: window.ID, Revision: window.Revision, Reason: "Window cancelled",
		IdempotencyKey: "cancel-revision-order", RequestID: "cancel-revision-order", Now: f.clock,
	})
	if err != nil || cancel.Revision != 2 || !cancel.UpdatedAt.Before(window.UpdatedAt) {
		t.Fatalf("fixture did not commit a later revision with an earlier clock: %+v, %v", cancel, err)
	}
	if witness := serviceSuppressionAt(t, f, f.ids[0], window.Start); witness != nil {
		t.Fatalf("older revision overrode the cancellation: %+v", witness)
	}
}
