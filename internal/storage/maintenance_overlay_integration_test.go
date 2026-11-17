//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"
)

func TestIntegrationMaintenanceOverlayCursorAndCurrentScope(t *testing.T) {
	f := newMaintenanceFixture(t)
	input := f.input("first")
	ctx, database := t.Context(), f.f.databaseURL
	first, err := CreateMaintenance(ctx, database, input)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := f.input("second")
	secondInput.Start = input.Start.Add(time.Minute)
	second, err := CreateMaintenance(ctx, database, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	list := ListSloMaintenanceInput{
		ListMaintenanceInput: ListMaintenanceInput{Proof: f.proof, Limit: 1, Now: f.clock},
		SloID:                f.ids[0], Start: first.Start, End: first.End,
	}
	page, err := ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextCursor == "" {
		t.Fatalf("initial page %+v: %v", page, err)
	}
	list.Cursor = page.NextCursor
	page, err = ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor != "" {
		t.Fatalf("next page %+v: %v", page, err)
	}
	wrongRange := list
	wrongRange.End = list.End.Add(time.Minute)
	if _, err = ListSloMaintenance(ctx, database, wrongRange); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("range cursor accepted: %v", err)
	}
	wrongSlo := list
	wrongSlo.SloID = f.ids[1]
	if _, err = ListSloMaintenance(ctx, database, wrongSlo); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("SLO cursor accepted: %v", err)
	}

	edit := input
	edit.ID, edit.Revision, edit.IdempotencyKey = first.ID, first.Revision, "change-scope"
	edit.SloIDs = []string{f.ids[1]}
	if _, err := EditMaintenance(ctx, database, edit); err != nil {
		t.Fatal(err)
	}
	list.Cursor = ""
	page, err = ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatalf("old scope visible after edit: %+v %v", page, err)
	}
	list.SloID = f.ids[1]
	page, err = ListSloMaintenance(ctx, database, list)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.Items[0].Revision != 2 {
		t.Fatalf("new scope omitted: %+v %v", page, err)
	}
}
