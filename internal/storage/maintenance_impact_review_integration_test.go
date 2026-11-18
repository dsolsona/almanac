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

	legacy := agentCursor{Version: 1, Kind: "maintenance-impact", AgentID: window.ID + "/1",
		ID: first.Items[0].SloID, Time: first.Items[0].Start, SnapshotAt: first.InventoryAt}
	input.Cursor, err = encodeAgentCursor(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Errorf("old same-window continuation should request reload: %v", err)
	}

	plain := legacy
	plain.AgentID = window.ID
	plain.MaintenanceSnapshot = &maintenanceCursorSnapshot{LegacyAuthorityRevision: new(int64(1)), WindowRevision: window.Revision}
	input.Cursor, err = encodeAgentCursor(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Errorf("previous plain-window cursor should request reload: %v", err)
	}
	wrongWindow := f.ids[0]
	legacy.AgentID = wrongWindow + "/1"
	input.Cursor, err = encodeAgentCursor(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Errorf("cursor from a different window must stay invalid: %v", err)
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

func TestIntegrationMaintenanceImpactTracksOnlyRelevantChanges(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	ctx := t.Context()
	create := f.input("impact-membership")
	create.ScopeKind, create.SloIDs = MaintenanceScopeAllServices, nil
	create.Start, create.End = f.at.Add(5*time.Minute), f.at.Add(10*time.Minute)
	window, err := CreateMaintenance(ctx, f.f.databaseURL, create)
	if err != nil {
		t.Fatal(err)
	}
	f.now = window.Start
	if err := f.runtime.runServiceMaintenanceProjection(ctx, 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	input := MaintenanceImpactInput{Proof: f.proof, ID: window.ID, Now: f.clock, Limit: 1}
	first, err := ReadMaintenanceImpact(ctx, f.f.databaseURL, input)
	if err != nil || !first.Ready || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("original impact: %+v %v", first, err)
	}
	previewInput := MaintenanceScopePreviewInput{Proof: f.proof, ScopeKind: MaintenanceScopeAllServices, Now: f.clock, Limit: 1}
	preview, err := PreviewMaintenanceScope(ctx, f.f.databaseURL, previewInput)
	if err != nil || len(preview.Items) != 1 || preview.NextCursor == "" {
		t.Fatalf("original inventory: %+v %v", preview, err)
	}
	create.IdempotencyKey, create.RequestID = "unrelated-window", "unrelated-window"
	create.Start, create.End = window.End.Add(time.Hour), window.End.Add(2*time.Hour)
	if _, err := CreateMaintenance(ctx, f.f.databaseURL, create); err != nil {
		t.Fatal(err)
	}
	wantNext := f.ids[0]
	if first.Items[0].SloID == wantNext {
		wantNext = f.ids[1]
	}
	input.Cursor, previewInput.Cursor = first.NextCursor, preview.NextCursor
	next, err := ReadMaintenanceImpact(ctx, f.f.databaseURL, input)
	if err != nil || len(next.Items) != 1 || next.Items[0].SloID != wantNext || next.NextCursor != "" {
		t.Fatalf("unrelated window interrupted or changed impact pagination: %+v %v", next, err)
	}
	nextPreview, err := PreviewMaintenanceScope(ctx, f.f.databaseURL, previewInput)
	if err != nil || len(nextPreview.Items) != 1 || nextPreview.NextCursor != "" {
		t.Fatalf("unrelated window interrupted preview pagination: %+v %v", nextPreview, err)
	}
	f.now = window.Start.Add(time.Minute)
	_, joined, err := CreateObjective(ctx, f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "impact-new-member", DisplayName: "Impact new member",
		ServiceID: first.Items[0].ServiceID, SliID: f.sliID, Target: "0.99",
		RollingWindowSeconds: 3600, IdempotencyKey: "impact-new-member", RequestID: "impact-new-member", Now: f.clock,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := ReadMaintenanceImpact(ctx, f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Errorf("late membership did not invalidate the old cursor: %+v %v", page, err)
	}
	input.Cursor, input.Limit = "", 10
	pending, err := ReadMaintenanceImpact(ctx, f.f.databaseURL, input)
	if err != nil || pending.Ready {
		t.Fatalf("pending membership was advertised as ready: %+v %v", pending, err)
	}
	if err := f.runtime.runServiceMaintenanceProjection(ctx, 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	complete, err := ReadMaintenanceImpact(ctx, f.f.databaseURL, input)
	if err != nil || !complete.Ready || complete.Total != 3 {
		t.Fatalf("completed membership was not visible: %+v %v", complete, err)
	}
	for _, item := range complete.Items {
		if item.SloID == joined.ID {
			return
		}
	}
	t.Fatalf("completed inventory omitted joined SLO %s", joined.ID)
}

func TestIntegrationExplicitMaintenanceImpactRevisionFences(t *testing.T) {
	t.Parallel()
	for _, cancel := range []bool{false, true} {
		name := "shortened-current"
		if cancel {
			name = "cancelled-before-start"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			ctx := t.Context()
			request := f.input("explicit-impact-revision")
			request.SloIDs = f.ids
			request.Start, request.End = f.at.Add(5*time.Minute), f.at.Add(10*time.Minute)
			window, err := CreateMaintenance(ctx, f.f.databaseURL, request)
			if err != nil {
				t.Fatal(err)
			}
			input := MaintenanceImpactInput{Proof: f.proof, ID: window.ID, Now: f.clock, Limit: 1}
			if cancel {
				input.Revision = window.Revision
			} else {
				f.now = window.Start.Add(time.Minute)
			}
			first, err := ReadMaintenanceImpact(ctx, f.f.databaseURL, input)
			if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
				t.Fatalf("explicit impact first page: %+v %v", first, err)
			}
			if cancel {
				_, err = CancelMaintenance(ctx, f.f.databaseURL, MaintenanceCommandInput{
					Proof: f.proof, ID: window.ID, Revision: window.Revision, Reason: "Postpone",
					IdempotencyKey: "cancel-explicit-impact", RequestID: "cancel-explicit-impact", Now: f.clock,
				})
			} else {
				_, err = EndMaintenance(ctx, f.f.databaseURL, EndMaintenanceInput{
					Proof: f.proof, ID: window.ID, Revision: window.Revision, Reason: "Done",
					IdempotencyKey: "shorten-explicit-impact", RequestID: "shorten-explicit-impact",
					End: window.Start.Add(2 * time.Minute), Now: f.clock,
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			input.Cursor = first.NextCursor
			if got, err := ReadMaintenanceImpact(ctx, f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
				t.Fatalf("explicit mutation left old cursor valid: %+v %v", got, err)
			}
			input.Cursor, input.Revision = "", window.Revision
			historic, err := ReadMaintenanceImpact(ctx, f.f.databaseURL, input)
			if err != nil || !historic.Ready || cancel && (len(historic.Items) != 0 || historic.NextCursor != "") {
				t.Fatalf("fresh historical revision: %+v %v", historic, err)
			}
		})
	}
}
