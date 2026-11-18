//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"
)

func TestIntegrationMaintenanceFutureImpactCompletionAndCancellation(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	request := f.input("impact-review")
	request.ScopeKind, request.SloIDs = MaintenanceScopeAllServices, nil
	request.Start, request.End = f.at.Add(5*time.Minute), f.at.Add(10*time.Minute)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, request)
	if err != nil {
		t.Fatal(err)
	}
	input := MaintenanceImpactInput{Proof: f.proof, ID: window.ID, Now: f.clock, Limit: 1}
	first, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, input)
	if err != nil || first.Ready || first.NextCursor == "" {
		t.Fatalf("scheduled impact: %+v %v", first, err)
	}
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	input.Cursor = first.NextCursor
	if page, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Errorf("continuation crossed projection completion: %+v %v", page, err)
	}
	projected, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, MaintenanceImpactInput{
		Proof: f.proof, ID: window.ID, Now: f.clock, Limit: 1,
	})
	if err != nil || projected.Ready || projected.NextCursor == "" {
		t.Fatalf("projected future window: %+v %v", projected, err)
	}
	input.Cursor, input.Revision = projected.NextCursor, window.Revision
	if next, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, input); err != nil || len(next.Items) != 1 {
		t.Errorf("continuation with matching explicit revision: %+v %v", next, err)
	}
	_, err = CancelMaintenance(t.Context(), f.f.databaseURL, MaintenanceCommandInput{
		Scoped: true, Proof: f.proof, ID: window.ID, Revision: window.Revision, Reason: "Cancelled before start",
		IdempotencyKey: "cancel-impact-review", RequestID: "cancel-impact-review", Now: f.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Cursor = ""
	historical, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, input)
	if err != nil || !historical.Ready || historical.Total != 0 || len(historical.Items) != 0 || historical.NextCursor != "" {
		t.Errorf("cancelled-before-start revision must be terminal and empty: %+v %v", historical, err)
	}
}
